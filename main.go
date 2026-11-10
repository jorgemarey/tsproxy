package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"tailscale.com/tsnet"
)

// parseArgs parses flags anywhere in args, unlike fs.Parse which stops at the
// first positional argument, so "-local" can follow mappings. Arguments after
// "--" are always positional.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if consumed := len(args) - len(rest); consumed > 0 && args[consumed-1] == "--" {
			return append(positional, rest...), nil
		}
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

func main() {
	fs := flag.NewFlagSet("tsproxy", flag.ContinueOnError)
	cfg := &serveConfig{}
	cfg.configureByFlags(fs)

	args, err := parseArgs(fs, os.Args[1:])
	if err != nil {
		// The flag package has already printed the error and usage.
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		os.Exit(2)
	}

	if cfg.version {
		fmt.Printf("tsproxy v%s\n", GetHumanVersion())
		os.Exit(0)
	}

	if len(args) == 0 && len(cfg.local) == 0 {
		fmt.Fprintln(os.Stderr, "At least one mapping must be provided")
		fs.Usage()
		os.Exit(1)
	}
	mappings, err := parseMappings(args, cfg.local, cfg.reservedPorts())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	logger := cfg.logger()
	srv, err := cfg.createServer(logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating proxy server: %s\n", err)
		os.Exit(1)
	}

	// Stop on Ctrl+C and on SIGTERM (what service managers and container
	// runtimes send).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var health *healthServer
	if cfg.healthAddr != "" {
		health = newHealthServer(srv)
	}
	closeHealth := func() {
		if health != nil {
			health.close()
		}
	}

	fail := func(msg string, args ...any) {
		logger.Error(msg, args...)
		closeHealth()
		shutdown(logger, cfg, srv)
		os.Exit(1)
	}

	// Serve health checks while connecting, so /healthz answers right away.
	if health != nil {
		if err := health.start(logger, cfg.healthAddr); err != nil {
			fail("Can't serve health checks", "error", err)
		}
	}

	// Wait for the node to connect before listening, so a bad authkey or an
	// unreachable control server fails at startup instead of on the first
	// connection. TLS listeners also need it to get their certificate.
	logger.Info("Connecting to the tailnet")
	upCtx, cancelUp := ctx, context.CancelFunc(func() {})
	if cfg.upTimeout > 0 {
		upCtx, cancelUp = context.WithTimeout(ctx, cfg.upTimeout)
	}
	status, err := srv.Up(upCtx)
	cancelUp()
	switch {
	case ctx.Err() != nil:
		logger.Info("Shutting down")
		closeHealth()
		shutdown(logger, cfg, srv)
		return
	case errors.Is(upCtx.Err(), context.DeadlineExceeded):
		fail(fmt.Sprintf("Timed out after %s connecting to the tailnet", cfg.upTimeout))
	case err != nil:
		fail("Can't connect to the tailnet", "error", err)
	}
	var name string
	if status.Self != nil {
		name = strings.TrimSuffix(status.Self.DNSName, ".")
	}
	logger.Info("Connected to the tailnet", "name", name, "ips", status.TailscaleIPs)

	listeners := make([]net.Listener, 0, len(mappings))
	closeListeners := func() {
		for _, ln := range listeners {
			ln.Close()
		}
	}
	for _, m := range mappings {
		ln, err := listen(srv, m)
		if err != nil {
			closeListeners()
			fail("Can't listen", "mapping", m.String(), "error", err)
		}
		listeners = append(listeners, ln)
	}

	go func() {
		<-ctx.Done()
		// Restore the default signal handling so a second signal exits
		// immediately instead of waiting for connections to finish.
		stop()
		logger.Info("Shutting down")
		if health != nil {
			health.listening.Store(false)
		}
		closeListeners()
	}()

	var conns connTracker
	var wg sync.WaitGroup
	for i, m := range mappings {
		mLogger := logger.With("mapping", m.String())
		mLogger.Info("Proxy is listening")
		wg.Go(func() { serve(mLogger, listeners[i], dialer(srv, m), &conns) })
	}
	if health != nil && ctx.Err() == nil {
		health.listening.Store(true)
	}
	wg.Wait()

	if n := conns.active(); n > 0 {
		logger.Info("Waiting for open connections to finish", "connections", n, "timeout", cfg.shutdownTimeout)
		if !conns.wait(cfg.shutdownTimeout) {
			logger.Warn("Closing connections still open after the shutdown timeout", "connections", conns.active())
		}
	}
	closeHealth()
	shutdown(logger, cfg, srv)
}

// connTracker tracks the connections being proxied so shutdown can wait for
// them to finish.
type connTracker struct {
	wg sync.WaitGroup
	n  atomic.Int64
}

// goHandle runs f in a new goroutine, tracking it as an open connection.
func (t *connTracker) goHandle(f func()) {
	t.n.Add(1)
	t.wg.Go(func() {
		defer t.n.Add(-1)
		f()
	})
}

// active returns the number of open connections.
func (t *connTracker) active() int64 { return t.n.Load() }

// wait waits up to timeout for all connections to finish and reports
// whether they did.
func (t *connTracker) wait(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		t.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// shutdown disconnects from the tailnet. Ephemeral nodes log out first so the
// control server removes them right away; otherwise the device lingers until
// it times out and a restarted proxy gets a suffixed name like "plex-1".
func shutdown(logger *slog.Logger, cfg *serveConfig, srv *tsnet.Server) {
	if cfg.ephemeral() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if lc, err := srv.LocalClient(); err != nil {
			logger.Warn("Can't get tailscale local client to log out", "error", err)
		} else if err := lc.Logout(ctx); err != nil {
			logger.Warn("Error logging out ephemeral node", "error", err)
		}
	}
	// Close also flushes tailscale logs.
	if err := srv.Close(); err != nil {
		logger.Warn("Error shutting down tailscale", "error", err)
	}
	if err := cfg.cleanup(); err != nil {
		logger.Warn("Error removing temporary directory", "error", err)
	}
}

const maxAcceptDelay = time.Second

// serve accepts connections on ln and proxies them using dial. It returns
// once ln is closed. Other accept errors are retried with exponential backoff
// so a persistent failure doesn't turn into a busy loop.
func serve(logger *slog.Logger, ln net.Listener, dial dialFunc, conns *connTracker) {
	var delay time.Duration
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if delay == 0 {
				delay = 5 * time.Millisecond
			} else {
				delay = min(2*delay, maxAcceptDelay)
			}
			logger.Warn("Can't accept connection", "error", err, "retry_in", delay)
			time.Sleep(delay)
			continue
		}
		delay = 0
		thisLogger := logger.With("source", conn.RemoteAddr().String())
		conns.goHandle(func() { handleConn(thisLogger, conn, dial) })
	}
}
