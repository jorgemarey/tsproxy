package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"slices"
	"testing"
	"time"
)

// TestHandleConnHalfClose checks that a client can close its write side and
// still receive the full response from the destination.
func TestHandleConnHalfClose(t *testing.T) {
	const request, response = "request", "response that is sent after the request is fully read"

	// Destination: reads until EOF, then replies.
	dst, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	go func() {
		c, err := dst.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		got, _ := io.ReadAll(c)
		if string(got) != request {
			t.Errorf("destination got %q, want %q", got, request)
		}
		time.Sleep(50 * time.Millisecond)
		io.WriteString(c, response)
	}()

	// Proxy front end.
	front, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer front.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := front.Accept()
		if err != nil {
			return
		}
		handleConn(slog.New(slog.DiscardHandler), c, tcpDialer(dst.Addr().String()))
	}()

	client, err := net.Dial("tcp", front.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(client, request)
	client.(*net.TCPConn).CloseWrite()

	got, err := io.ReadAll(client)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != response {
		t.Fatalf("client got %q, want %q", got, response)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleConn did not return")
	}
}

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
		serve(slog.New(slog.DiscardHandler), ln, tcpDialer("127.0.0.1:1"))
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
	}{
		{args: []string{"a:1"}, wantPos: []string{"a:1"}},
		{args: []string{"-local", "db:5432", "a:1"}, wantPos: []string{"a:1"}, wantLocal: []string{"db:5432"}},
		{args: []string{"a:1", "-local", "db:5432", "b:2", "-local=c:3"}, wantPos: []string{"a:1", "b:2"}, wantLocal: []string{"db:5432", "c:3"}},
		{args: []string{"a:1", "--", "-local", "b:2"}, wantPos: []string{"a:1", "-local", "b:2"}},
		{args: []string{}, wantPos: nil},
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
	}
}
