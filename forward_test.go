package main

import (
	"io"
	"log/slog"
	"net"
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
