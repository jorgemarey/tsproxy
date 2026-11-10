package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"

	"tailscale.com/ipn/store/mem"
	"tailscale.com/tsnet"
)

type serveConfig struct {
	port        int
	version     bool
	hostname    string
	authKey     string
	storageDir  string
	controlURL  string
	disableLogs bool
	logLevel    slog.Level

	// tempDir is the temporary tsnet directory used by ephemeral nodes. It is
	// removed by cleanup.
	tempDir string
}

func (c *serveConfig) configureByFlags(fs *flag.FlagSet) {
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: tsproxy [options] destination\n")
		fs.PrintDefaults()
	}
	fs.IntVar(&c.port, "port", 0, "Port where the service will be available on the tailnet")
	fs.BoolVar(&c.version, "version", false, "Prints program version")
	fs.StringVar(&c.hostname, "hostname", "", "Name for the device this proxy represents")
	fs.StringVar(&c.authKey, "authkey", "", "Tailscale device authkey (defaults to the TS_AUTHKEY environment variable)")
	fs.StringVar(&c.storageDir, "dir", "", "State storage directory. If set, the device is persistent and keeps its identity across restarts. If not, it is ephemeral and removed from the tailnet on shutdown")
	fs.StringVar(&c.controlURL, "control-url", "", "Set if the coordination server is not the default")
	fs.BoolVar(&c.disableLogs, "disable-ts-logs", false, "Set to disable tailscale logs")
	fs.TextVar(&c.logLevel, "log-level", slog.LevelInfo, "Log level to use")
}

// servePort validates destination and returns the port to listen on in the
// tailnet: the -port flag if set, otherwise the destination's port.
func (c *serveConfig) servePort(destination string) (int, error) {
	_, destPort, err := net.SplitHostPort(destination)
	if err != nil {
		return 0, fmt.Errorf("invalid destination %q, expected host:port: %w", destination, err)
	}
	port, err := net.LookupPort("tcp", destPort)
	if err != nil || port == 0 {
		return 0, fmt.Errorf("invalid destination port %q", destPort)
	}
	if c.port != 0 {
		if c.port < 1 || c.port > 65535 {
			return 0, fmt.Errorf("invalid -port %d, must be between 1 and 65535", c.port)
		}
		return c.port, nil
	}
	return port, nil
}

func (c *serveConfig) createServer() (*tsnet.Server, error) {
	// The env var is read here rather than used as the flag default so the
	// key is never printed by the usage/help output.
	if c.authKey == "" {
		c.authKey = os.Getenv("TS_AUTHKEY")
	}
	if c.authKey == "" {
		return nil, fmt.Errorf("an authkey must be provided")
	}

	srv := &tsnet.Server{
		Hostname:   c.hostname,
		AuthKey:    c.authKey,
		Dir:        c.storageDir,
		ControlURL: c.controlURL,
		Ephemeral:  c.ephemeral(),
	}

	if srv.Ephemeral {
		// The node identity lives in memory. tsnet still needs a directory for
		// other files, so use a temporary one that cleanup removes.
		store, err := mem.New(nil, "")
		if err != nil {
			return nil, fmt.Errorf("error creating in-memory state store: %w", err)
		}
		srv.Store = store

		dir, err := os.MkdirTemp("", "tsproxy-")
		if err != nil {
			return nil, fmt.Errorf("error creating temporary directory: %w", err)
		}
		srv.Dir = dir
		c.tempDir = dir
	}

	if c.disableLogs {
		srv.Logf = func(string, ...any) {}
	}

	return srv, nil
}

// ephemeral reports whether the proxy runs as an ephemeral node. That is the
// case unless a state directory is given.
func (c *serveConfig) ephemeral() bool {
	return c.storageDir == ""
}

// cleanup removes the temporary directory created for an ephemeral node.
func (c *serveConfig) cleanup() error {
	if c.tempDir == "" {
		return nil
	}
	return os.RemoveAll(c.tempDir)
}

func (c *serveConfig) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: c.logLevel,
	}))
}
