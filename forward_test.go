package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
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
		handleConn(slog.New(slog.DiscardHandler), c, tcpDialer(dst.Addr().String()), func() bool { return false })
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

// recordHandler records the level of every log record.
type recordHandler struct {
	mu     sync.Mutex
	levels []slog.Level
}

func (h *recordHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *recordHandler) WithGroup(string) slog.Handler            { return h }
func (h *recordHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.levels = append(h.levels, r.Level)
	return nil
}

// warnings returns how many records were logged at warn level or above.
func (h *recordHandler) warnings() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, l := range h.levels {
		if l >= slog.LevelWarn {
			n++
		}
	}
	return n
}

// TestConnTrackerCloseAll checks that closeAll ends connections that are
// still open at the end of the shutdown timeout, without logging warnings.
func TestConnTrackerCloseAll(t *testing.T) {
	// Destination: keeps every connection open until the client is gone.
	dst, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	received := make(chan struct{})
	go func() {
		c, err := dst.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 1)
		if _, err := c.Read(buf); err == nil {
			close(received)
		}
		io.Copy(io.Discard, c)
	}()

	front, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer front.Close()
	logs := &recordHandler{}
	var conns connTracker
	served := make(chan struct{})
	go func() {
		defer close(served)
		serve(t.Context(), slog.New(logs), front, tcpDialer(dst.Addr().String()), &conns)
	}()

	client, err := net.Dial("tcp", front.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	io.WriteString(client, "x")
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("the connection was not proxied")
	}

	// Like run: stop accepting before waiting for the open connections.
	front.Close()
	<-served

	if conns.wait(50 * time.Millisecond) {
		t.Fatal("wait() returned true with a connection still open")
	}
	conns.closeAll()
	if !conns.wait(5 * time.Second) {
		t.Fatal("the connection handler did not return after closeAll")
	}
	client.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := client.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("client read error = %v, want EOF", err)
	}
	if n := logs.warnings(); n != 0 {
		t.Fatalf("logged %d warnings, want 0", n)
	}
}

// failingConn is a conn whose reads fail with an error other than
// net.ErrClosed, like a tailnet connection cut by the tailscale server
// shutting down.
type failingConn struct{ net.Conn }

func (failingConn) Read([]byte) (int, error) { return 0, errors.New("operation aborted") }

// TestPipeErrorLevel checks that forwarding errors are warnings, except
// during shutdown when they are expected.
func TestPipeErrorLevel(t *testing.T) {
	for _, shuttingDown := range []bool{false, true} {
		a, b := net.Pipe()
		logs := &recordHandler{}
		pipe(slog.New(logs), a, failingConn{b}, func() bool { return shuttingDown })
		want := 1
		if shuttingDown {
			want = 0
		}
		if n := logs.warnings(); n != want {
			t.Errorf("shuttingDown=%v: logged %d warnings, want %d", shuttingDown, n, want)
		}
		if len(logs.levels) != 1 {
			t.Errorf("shuttingDown=%v: logged %d records, want 1", shuttingDown, len(logs.levels))
		}
	}
}
