package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"tailscale.com/ipn/store/mem"
	"tailscale.com/tsnet"
)

const (
	defaultPort = 8080
)

type serveConfig struct {
	port        int
	version     bool
	hostname    string
	authKey     string
	storageDir  string
	controlURL  string
	ephemeral   bool
	disableLogs bool
	logLevel    slog.Level
}

func (c *serveConfig) configureByFlags(fs *flag.FlagSet) {
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: tsproxy [options] destination\n")
		fs.PrintDefaults()
	}
	fs.IntVar(&c.port, "port", 0, "Port where the service will be available on the tailnet")
	fs.BoolVar(&c.version, "version", false, "Prints program version")
	fs.StringVar(&c.hostname, "hostname", "", "Name for the device this proxy represents")
	fs.StringVar(&c.authKey, "authkey", os.Getenv("TS_AUTHKEY"), "Tailscale device authkey")
	fs.StringVar(&c.storageDir, "dir", "", "State storage directory")
	fs.StringVar(&c.controlURL, "control-url", "", "Set if the coordination server is not the default")
	fs.BoolVar(&c.ephemeral, "ephemeral", false, "Set to configure this proxy as an ephemeral device")
	fs.BoolVar(&c.disableLogs, "disable-ts-logs", false, "Set to disable tailscale logs")
	fs.TextVar(&c.logLevel, "log-level", slog.LevelInfo, "Log level to use")
}

func (c *serveConfig) servePort(logger *slog.Logger, destination string) int {
	if c.port > 0 {
		return c.port
	}

	parts := strings.Split(destination, ":")

	if len(parts) != 2 {
		return defaultPort
	}

	port, err := strconv.Atoi(parts[1])
	if err != nil {
		logger.Warn(fmt.Sprintf("Can't parse '%s' port", destination))
		return defaultPort
	}
	return port
}

func (c *serveConfig) createServer() (*tsnet.Server, error) {
	if c.authKey == "" {
		return nil, fmt.Errorf("an authkey must be provided")
	}

	srv := &tsnet.Server{
		Hostname:   c.hostname,
		AuthKey:    c.authKey,
		Dir:        c.storageDir,
		ControlURL: c.controlURL,
		Ephemeral:  c.ephemeral,
	}

	if srv.Dir == "" {
		dir, err := os.MkdirTemp(os.TempDir(), "tsproxy-state")
		if err != nil {
			return nil, fmt.Errorf("error creating temporary state directory: %s", err)
		}
		srv.Dir = dir
	}

	// if ephemeral we store the configuration in memory
	if srv.Ephemeral {
		store, _ := mem.New(nil, "")
		srv.Store = store
	}

	if c.disableLogs {
		srv.Logf = func(string, ...any) {}
	}

	return srv, nil
}

func (c *serveConfig) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: c.logLevel,
	}))
}
