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
// it and in until both directions are done. ctx is cancelled on shutdown,
// when connections still open are closed: it aborts the dial, and errors
// after that are expected and only logged at debug level.
func handleConn(ctx context.Context, logger *slog.Logger, in net.Conn, dial dialFunc) {
	logger.Debug("Accepted and handling incoming connection")
	defer logger.Debug("Closed incoming connection")
	defer in.Close()

	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	out, err := dial(dialCtx)
	cancel()
	switch {
	case err != nil && ctx.Err() != nil:
		logger.Debug("Dial destination stopped by shutdown", "error", err)
		return
	case err != nil:
		logger.Error("Dial destination failed", "error", err)
		return
	}
	defer out.Close()

	// Wait for both directions: one side finishing its writes (half-close)
	// must not cut off data still flowing the other way.
	var wg sync.WaitGroup
	wg.Go(func() { pipe(ctx, logger, out, in) })
	wg.Go(func() { pipe(ctx, logger, in, out) })
	wg.Wait()
}

// pipe copies src into dst. When src reaches EOF the write side of dst is
// closed so the peer sees the EOF too, while dst can keep sending data back.
// On any other error both conns are closed so the other direction doesn't
// hang. Errors after ctx is cancelled are expected and logged at debug.
func pipe(ctx context.Context, logger *slog.Logger, dst, src net.Conn) {
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		src.Close()
		switch {
		case errors.Is(err, net.ErrClosed):
			// The other direction already aborted.
		case ctx.Err() != nil:
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
