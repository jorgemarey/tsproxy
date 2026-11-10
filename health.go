package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"tailscale.com/ipn"
	"tailscale.com/tsnet"
)

// healthServer serves health checks over HTTP:
//   - /healthz answers 200 while the process is running.
//   - /readyz answers 200 while the proxy is listening and the device is
//     connected to the tailnet, and 503 otherwise, including during shutdown.
type healthServer struct {
	// listening is set once all listeners are open and cleared on shutdown.
	listening atomic.Bool
	// backendState returns the tailscale backend state, e.g. "Running".
	backendState func(ctx context.Context) (string, error)

	http *http.Server
}

func newHealthServer(srv *tsnet.Server) *healthServer {
	h := &healthServer{
		backendState: func(ctx context.Context) (string, error) {
			lc, err := srv.LocalClient()
			if err != nil {
				return "", err
			}
			st, err := lc.StatusWithoutPeers(ctx)
			if err != nil {
				return "", err
			}
			return st.BackendState, nil
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("GET /readyz", h.readyz)
	h.http = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return h
}

func (h *healthServer) readyz(w http.ResponseWriter, r *http.Request) {
	if !h.listening.Load() {
		http.Error(w, "not ready: proxy is not listening", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	state, err := h.backendState(ctx)
	if err != nil {
		http.Error(w, fmt.Sprintf("not ready: can't get tailscale state: %v", err), http.StatusServiceUnavailable)
		return
	}
	if state != ipn.Running.String() {
		http.Error(w, fmt.Sprintf("not ready: tailscale is %s", state), http.StatusServiceUnavailable)
		return
	}
	io.WriteString(w, "ok\n")
}

// start serves health checks on addr in the background.
func (h *healthServer) start(logger *slog.Logger, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	logger.Info("Serving health checks", "address", ln.Addr().String())
	go func() {
		if err := h.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("Health check server failed", "error", err)
		}
	}()
	return nil
}

func (h *healthServer) close() {
	h.http.Close()
}
