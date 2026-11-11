package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"testing"
	"time"
)

// flakyListener fails Accept a few times with a non-close error, then reports
// itself closed.
type flakyListener struct {
	net.Listener
	failures int
	calls    int
}

func (l *flakyListener) Accept() (net.Conn, error) {
	l.calls++
	if l.calls <= l.failures {
		return nil, errors.New("temporary failure")
	}
	return nil, fmt.Errorf("wrapped: %w", net.ErrClosed)
}

// TestServeStopsOnClose checks that serve retries other accept errors and
// returns once the listener is closed instead of looping forever.
func TestServeStopsOnClose(t *testing.T) {
	ln := &flakyListener{failures: 3}
	done := make(chan struct{})
	go func() {
		defer close(done)
		serve(t.Context(), slog.New(slog.DiscardHandler), ln, tcpDialer("127.0.0.1:1"), &connTracker{})
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after the listener was closed")
	}
	if ln.calls != ln.failures+1 {
		t.Fatalf("Accept called %d times, want %d", ln.calls, ln.failures+1)
	}
}

// failingListener always fails Accept with a non-close error, and closes
// waiting once it has been called n times.
type failingListener struct {
	net.Listener
	n       int
	calls   int
	waiting chan struct{}
}

func (l *failingListener) Accept() (net.Conn, error) {
	l.calls++
	if l.calls == l.n {
		close(l.waiting)
	}
	return nil, errors.New("temporary failure")
}

// TestServeStopsOnCancelDuringRetry checks that cancelling the context
// interrupts the wait before retrying a failed accept.
func TestServeStopsOnCancelDuringRetry(t *testing.T) {
	// After 8 failures serve has waited 5+10+...+320ms and starts a 640ms wait.
	ln := &failingListener{n: 8, waiting: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		serve(ctx, slog.New(slog.DiscardHandler), ln, tcpDialer("127.0.0.1:1"), &connTracker{})
	}()

	select {
	case <-ln.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not retry the failed accepts")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("serve did not return after the context was cancelled")
	}
}

func tcpDialer(addr string) dialFunc {
	return func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}
}

func TestParseArgs(t *testing.T) {
	tests := []struct {
		args      []string
		wantPos   []string
		wantLocal []string
		// wantTailnet is the -tailnet options; tailnetSpecs adds wantPos.
		wantTailnet []string
	}{
		{args: []string{"a:1"}, wantPos: []string{"a:1"}},
		{args: []string{"-local", "db:5432", "a:1"}, wantPos: []string{"a:1"}, wantLocal: []string{"db:5432"}},
		{args: []string{"a:1", "-local", "db:5432", "b:2", "-local=c:3"}, wantPos: []string{"a:1", "b:2"}, wantLocal: []string{"db:5432", "c:3"}},
		{args: []string{"a:1", "--", "-local", "b:2"}, wantPos: []string{"a:1", "-local", "b:2"}},
		{args: []string{}, wantPos: nil},
		{args: []string{"-tailnet", "80=a:8080", "b:2", "-local", "db:5432", "-tailnet=c:3"}, wantPos: []string{"b:2"}, wantTailnet: []string{"80=a:8080", "c:3"}, wantLocal: []string{"db:5432"}},
	}
	for _, tt := range tests {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		cfg := &serveConfig{}
		cfg.configureByFlags(fs)
		got, err := parseArgs(fs, tt.args)
		if err != nil {
			t.Errorf("parseArgs(%q): %v", tt.args, err)
			continue
		}
		if !slices.Equal(got, tt.wantPos) || !slices.Equal([]string(cfg.local), tt.wantLocal) {
			t.Errorf("parseArgs(%q) = %q, local %q; want %q, local %q", tt.args, got, cfg.local, tt.wantPos, tt.wantLocal)
		}
		if want := slices.Concat(tt.wantTailnet, tt.wantPos); !slices.Equal(cfg.tailnetSpecs(got), want) {
			t.Errorf("parseArgs(%q): tailnetSpecs = %q, want %q", tt.args, cfg.tailnetSpecs(got), want)
		}
	}
}

// TestConnTrackerWait checks that shutdown waits for open connections and
// gives up after the timeout.
func TestConnTrackerWait(t *testing.T) {
	var conns connTracker
	release := make(chan struct{})
	c, peer := net.Pipe()
	defer c.Close()
	defer peer.Close()
	conns.goHandle(c, func(context.Context) { <-release })

	if n := conns.active(); n != 1 {
		t.Fatalf("active() = %d, want 1", n)
	}
	if conns.wait(50 * time.Millisecond) {
		t.Fatal("wait() returned true with a connection still open")
	}
	close(release)
	if !conns.wait(5 * time.Second) {
		t.Fatal("wait() returned false after the connection finished")
	}
	if n := conns.active(); n != 0 {
		t.Fatalf("active() = %d, want 0", n)
	}
}

func TestShutdownBudget(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		timeout            time.Duration
		wantDrain, wantOut time.Duration // after start
	}{
		{timeout: 5 * time.Second, wantDrain: 4 * time.Second, wantOut: 4750 * time.Millisecond},
		{timeout: 30 * time.Second, wantDrain: 29 * time.Second, wantOut: 29750 * time.Millisecond},
		// Short timeouts keep half for leaving the tailnet.
		{timeout: time.Second, wantDrain: 500 * time.Millisecond, wantOut: 875 * time.Millisecond},
	}
	for _, tt := range tests {
		b := newShutdownBudget(start, tt.timeout)
		if got := b.drainDeadline().Sub(start); got != tt.wantDrain {
			t.Errorf("timeout %s: drain deadline after %s, want %s", tt.timeout, got, tt.wantDrain)
		}
		if got := b.logoutDeadline().Sub(start); got != tt.wantOut {
			t.Errorf("timeout %s: logout deadline after %s, want %s", tt.timeout, got, tt.wantOut)
		}
		if b.deadline.Sub(start) != tt.timeout {
			t.Errorf("timeout %s: deadline after %s", tt.timeout, b.deadline.Sub(start))
		}
	}
}
