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
	"syscall"
	"time"

	"tailscale.com/tsnet"
)

// parseArgs parses flags anywhere in args, unlike fs.Parse which stops at the
// first positional argument, so "-tailnet" and "-local" can follow mappings. Arguments after
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

	tailnetSpecs := cfg.tailnetSpecs(args)
	if len(tailnetSpecs) == 0 && len(cfg.local) == 0 {
		fmt.Fprintln(os.Stderr, "At least one mapping must be provided")
		fs.Usage()
		os.Exit(1)
	}
	mappings, err := parseMappings(tailnetSpecs, cfg.local, cfg.reservedPorts())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if cfg.shutdownTimeout <= 0 {
		fmt.Fprintln(os.Stderr, "-shutdown-timeout must be greater than 0")
		os.Exit(1)
	}

	logger := cfg.logger()
	srv, err := cfg.createServer(logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating proxy server: %s\n", err)
		os.Exit(1)
	}
	os.Exit(run(logger, cfg, srv, mappings))
}

// run connects srv to the tailnet and proxies mappings until a signal
// arrives, then waits for open connections and shuts down. It returns the
// process exit code.
func run(logger *slog.Logger, cfg *serveConfig, srv *tsnet.Server, mappings []mapping) int {
	// Stop on Ctrl+C and on SIGTERM (what service managers and container
	// runtimes send).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// The -shutdown-timeout budget starts with the shutdown: on the first
	// signal, or when run returns early because of an error.
	beginShutdown := sync.OnceValue(func() shutdownBudget {
		return newShutdownBudget(time.Now(), cfg.shutdownTimeout)
	})
	defer func() { shutdown(logger, cfg, srv, beginShutdown().logoutDeadline()) }()
	// Deferred after shutdown so it runs first: once shutting down, a second
	// signal exits immediately, also during an early shutdown (a signal while
	// connecting or a startup error).
	defer stop()

	var health *healthServer
	if cfg.healthAddr != "" {
		health = newHealthServer(srv)
		defer health.close()
		// Serve health checks while connecting, so /healthz answers right away.
		if err := health.start(logger, cfg.healthAddr); err != nil {
			logger.Error("Can't serve health checks", "error", err)
			return 1
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
		return 0
	case err != nil && errors.Is(upCtx.Err(), context.DeadlineExceeded):
		logger.Error(fmt.Sprintf("Timed out after %s connecting to the tailnet", cfg.upTimeout))
		return 1
	case err != nil:
		logger.Error("Can't connect to the tailnet", "error", err)
		return 1
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
			logger.Error("Can't listen", "mapping", m.String(), "error", err)
			return 1
		}
		listeners = append(listeners, ln)
	}

	// Mark the proxy ready before watching for shutdown, so the shutdown
	// below always clears it afterwards.
	if health != nil {
		health.listening.Store(true)
	}
	go func() {
		<-ctx.Done()
		beginShutdown()
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
		wg.Go(func() { serve(ctx, mLogger, listeners[i], dialer(srv, m), &conns) })
	}
	wg.Wait()

	budget := beginShutdown()
	if n := conns.active(); n > 0 {
		wait := time.Until(budget.drainDeadline())
		logger.Info("Waiting for open connections to finish", "connections", n, "timeout", wait.Round(time.Millisecond))
		if !conns.wait(wait) {
			logger.Warn("Closing connections still open at the shutdown timeout", "connections", conns.active())
			// Close them before leaving the tailnet, and let their handlers
			// finish so they don't log errors about the tailnet going away.
			conns.closeAll()
			conns.wait(closeTimeout)
		}
	}
	return 0
}

// closeTimeout bounds how long shutdown waits for handlers to return after
// their connections are closed. Closing a connection or cancelling its dial
// makes the handler return right away, so this is only a backstop.
const closeTimeout = 100 * time.Millisecond

// leaveReserve is the part of -shutdown-timeout kept for closing the
// connections still open and leaving the tailnet, at most half of it.
const leaveReserve = time.Second

// shutdownBudget splits -shutdown-timeout, counted from the start of the
// shutdown, between waiting for open connections to finish and leaving the
// tailnet, so the whole shutdown fits in it.
type shutdownBudget struct {
	// deadline is when the shutdown must be done.
	deadline time.Time
	// reserve is the time before deadline kept for leaving the tailnet.
	reserve time.Duration
}

func newShutdownBudget(start time.Time, timeout time.Duration) shutdownBudget {
	return shutdownBudget{deadline: start.Add(timeout), reserve: min(leaveReserve, timeout/2)}
}

// drainDeadline is when connections still open are closed.
func (b shutdownBudget) drainDeadline() time.Time { return b.deadline.Add(-b.reserve) }

// logoutDeadline is when logging out gives up. The last quarter of the
// reserve is left for closing the tailscale server.
func (b shutdownBudget) logoutDeadline() time.Time { return b.deadline.Add(-b.reserve / 4) }

// connTracker tracks the connections being proxied so shutdown can wait for
// them to finish, and close them if they don't.
type connTracker struct {
	wg    sync.WaitGroup
	mu    sync.Mutex
	conns map[net.Conn]struct{}
	// ctx is passed to the handlers and cancelled by closeAll.
	ctx    context.Context
	cancel context.CancelFunc
}

// init sets up t on first use. t.mu must be held.
func (t *connTracker) init() {
	if t.conns == nil {
		t.conns = make(map[net.Conn]struct{})
		t.ctx, t.cancel = context.WithCancel(context.Background())
	}
}

// goHandle runs f in a new goroutine, tracking c as an open connection until
// f returns. f's context is cancelled by closeAll.
func (t *connTracker) goHandle(c net.Conn, f func(ctx context.Context)) {
	t.mu.Lock()
	t.init()
	t.conns[c] = struct{}{}
	ctx := t.ctx
	t.mu.Unlock()
	t.wg.Go(func() {
		defer func() {
			t.mu.Lock()
			delete(t.conns, c)
			t.mu.Unlock()
		}()
		f(ctx)
	})
}

// active returns the number of open connections.
func (t *connTracker) active() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.conns)
}

// closeAll closes all open connections and cancels the handlers' context,
// which also aborts the ones still connecting to their destination.
func (t *connTracker) closeAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.init()
	t.cancel()
	for c := range t.conns {
		c.Close()
	}
}

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
// Logging out gives up at logoutDeadline.
func shutdown(logger *slog.Logger, cfg *serveConfig, srv *tsnet.Server, logoutDeadline time.Time) {
	if cfg.ephemeral() {
		ctx, cancel := context.WithDeadline(context.Background(), logoutDeadline)
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
// once ln is closed or ctx is done. Other accept errors are retried with
// exponential backoff so a persistent failure doesn't turn into a busy loop.
func serve(ctx context.Context, logger *slog.Logger, ln net.Listener, dial dialFunc, conns *connTracker) {
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
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			continue
		}
		delay = 0
		thisLogger := logger.With("source", conn.RemoteAddr().String())
		conns.goHandle(conn, func(ctx context.Context) { handleConn(ctx, thisLogger, conn, dial) })
	}
}
