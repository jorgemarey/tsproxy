package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthServer(t *testing.T) {
	h := newHealthServer(nil)
	state, stateErr := "Running", error(nil)
	h.backendState = func(context.Context) (string, error) { return state, stateErr }

	get := func(path string) int {
		rec := httptest.NewRecorder()
		h.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code
	}

	if code := get("/healthz"); code != http.StatusOK {
		t.Errorf("/healthz = %d, want 200", code)
	}
	if code := get("/readyz"); code != http.StatusServiceUnavailable {
		t.Errorf("/readyz before listening = %d, want 503", code)
	}

	h.listening.Store(true)
	if code := get("/readyz"); code != http.StatusOK {
		t.Errorf("/readyz while listening and running = %d, want 200", code)
	}

	state = "Starting"
	if code := get("/readyz"); code != http.StatusServiceUnavailable {
		t.Errorf("/readyz while tailscale is starting = %d, want 503", code)
	}

	state, stateErr = "", errors.New("boom")
	if code := get("/readyz"); code != http.StatusServiceUnavailable {
		t.Errorf("/readyz when the state can't be read = %d, want 503", code)
	}

	state, stateErr = "Running", nil
	h.listening.Store(false) // shutting down
	if code := get("/readyz"); code != http.StatusServiceUnavailable {
		t.Errorf("/readyz while shutting down = %d, want 503", code)
	}
}
