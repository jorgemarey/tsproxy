package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

// dialTimeout bounds how long connecting to a destination can take.
const dialTimeout = 10 * time.Second

// handleConn connects to the destination with dial and forwards data between
// it and in until both directions are done. Once shuttingDown returns true,
// forwarding errors are expected and only logged at debug level.
func handleConn(logger *slog.Logger, in net.Conn, dial dialFunc, shuttingDown func() bool) {
	logger.Debug("Accepted and handling incoming connection")
	defer logger.Debug("Closed incoming connection")
	defer in.Close()

	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	out, err := dial(ctx)
	cancel()
	if err != nil {
		logger.Error("Dial destination failed", "error", err)
		return
	}
	defer out.Close()

	// Wait for both directions: one side finishing its writes (half-close)
	// must not cut off data still flowing the other way.
	var wg sync.WaitGroup
	wg.Go(func() { pipe(logger, out, in, shuttingDown) })
	wg.Go(func() { pipe(logger, in, out, shuttingDown) })
	wg.Wait()
}

// pipe copies src into dst. When src reaches EOF the write side of dst is
// closed so the peer sees the EOF too, while dst can keep sending data back.
// On any other error both conns are closed so the other direction doesn't
// hang.
func pipe(logger *slog.Logger, dst, src net.Conn, shuttingDown func() bool) {
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		src.Close()
		switch {
		case errors.Is(err, net.ErrClosed):
			// The other direction already aborted.
		case shuttingDown():
			logger.Debug("Forwarding stopped by shutdown", "error", err)
		default:
			logger.Warn("Unexpected error when forwarding data", "error", err)
		}
		return
	}
	closeWrite(dst)
}

// closeWrite shuts down the write side of c, or closes it entirely if it
// doesn't support half-closing.
func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
		return
	}
	c.Close()
}
