package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"os"
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

	if fs.NArg() > 1 {
		fmt.Println("Only one positional argument can be provided")
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

	ln, err := srv.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Fatal(err)
	}
	defer ln.Close()

	logger.Info(fmt.Sprintf("Proxy server is running on address %s", ln.Addr().String()))
	for {
		conn, err := ln.Accept()
		if err != nil {
			logger.Warn("Can't accept connection", "error", err)
			continue
		}
		thisLogger := logger.With("source", conn.RemoteAddr().String())
		go handleConn(thisLogger, conn, destination)
	}
}
