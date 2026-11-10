package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func handleConn(logger *slog.Logger, in net.Conn, destination string) {
	logger.Debug("Accepted and handling incomming connection")
	defer logger.Debug("Closed incomming connection")
	defer in.Close()

	out, err := net.Dial("tcp", destination)
	if err != nil {
		logger.Error("Dial remote failed", "remote", destination, "error", err)
		return
	}
	defer out.Close()

	errCh := make(chan error, 2)
	go proxy(out, in, errCh)
	go proxy(in, out, errCh)
	// Wait for both directions: one side finishing its writes (half-close)
	// must not cut off data still flowing the other way.
	for range 2 {
		if err := <-errCh; err != nil && !errors.Is(err, net.ErrClosed) {
			logger.Warn("Unexpected error when forwarding data", "error", err)
		}
	}
}

// proxy copies src into dst. When src reaches EOF the write side of dst is
// closed so the peer sees the EOF too, while dst can keep sending data back.
func proxy(dst, src net.Conn, errCh chan<- error) {
	_, err := io.Copy(dst, src)
	if err != nil {
		// Abort the whole connection so the other direction doesn't hang.
		dst.Close()
		src.Close()
	} else if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	} else {
		dst.Close()
	}
	errCh <- err
}

func main() {
	fs := flag.NewFlagSet("tsproxy", flag.ContinueOnError)
	cfg := &serveConfig{}
	cfg.configureByFlags(fs)

	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(1)
	}

	if cfg.version {
		fmt.Printf("tsproxy v%s\n", GetHumanVersion())
		os.Exit(0)
	}

	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "Exactly one destination (host:port) must be provided")
		fs.Usage()
		os.Exit(1)
	}
	destination := fs.Arg(0)

	srv, err := cfg.createServer()
	if err != nil {
		fmt.Printf("Error creating proxy server: %s", err)
		os.Exit(1)
	}

	logger := cfg.logger()
	port := cfg.servePort(logger, destination)

	// Stop on Ctrl+C and on SIGTERM (what Docker and Kubernetes send).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := srv.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		logger.Error("Can't listen on the tailnet", "error", err)
		srv.Close()
		os.Exit(1)
	}

	go func() {
		<-ctx.Done()
		logger.Info("Shutting down")
		ln.Close()
	}()

	logger.Info(fmt.Sprintf("Proxy server is running on address %s", ln.Addr().String()))
	serve(logger, ln, destination)

	// Disconnect from the tailnet cleanly and flush tailscale logs.
	if err := srv.Close(); err != nil {
		logger.Warn("Error shutting down tailscale", "error", err)
	}
}

const maxAcceptDelay = time.Second

// serve accepts connections on ln and proxies them to destination. It returns
// once ln is closed. Other accept errors are retried with exponential backoff
// so a persistent failure doesn't turn into a busy loop.
func serve(logger *slog.Logger, ln net.Listener, destination string) {
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
		go handleConn(thisLogger, conn, destination)
	}
}
