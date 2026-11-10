package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"tailscale.com/envknob"
	"tailscale.com/ipn/store/mem"
	"tailscale.com/tsnet"
	"tailscale.com/types/logger"
)

const usage = `Usage: tsproxy [options] [MAPPING...]

Exposes local services on the tailnet (MAPPING arguments) and tailnet
services locally (-local options). At least one mapping is required.

MAPPING, from the tailnet to a local service: [PORT=]DEST[,tls]
  DEST is HOST:PORT or unix:/path/to.sock. PORT is the port on the tailnet;
  it defaults to DEST's port and is required for unix sockets. With ",tls"
  the port serves TLS using the device's Tailscale certificate (HTTPS and
  MagicDNS must be enabled in the tailnet).

-local, from a local listener to a tailnet service: [LISTEN=]HOST:PORT
  HOST:PORT is an address on the tailnet. LISTEN is a PORT (bound to
  127.0.0.1), HOST:PORT or unix:/path/to.sock; it defaults to 127.0.0.1
  and the tailnet port.

Examples:
  tsproxy -hostname plex 127.0.0.1:32400
  tsproxy -hostname web 80=127.0.0.1:8080 443=127.0.0.1:8080,tls 9000=unix:/run/app.sock
  tsproxy -hostname app -local 5432=db:5432 -local unix:/tmp/redis.sock=cache:6379

Options:
`

// stringList is a flag.Value collecting every use of a repeatable flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ", ") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

type serveConfig struct {
	version     bool
	hostname    string
	authKey     string
	storageDir  string
	controlURL  string
	noLogUpload bool
	logLevel    slog.Level
	local       stringList

	// tempDir is the temporary tsnet directory used by ephemeral nodes. It is
	// removed by cleanup.
	tempDir string
}

func (c *serveConfig) configureByFlags(fs *flag.FlagSet) {
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), usage)
		fs.PrintDefaults()
	}
	fs.Var(&c.local, "local", "Expose a tailnet service locally: [LISTEN=]HOST:PORT. Can be repeated")
	fs.BoolVar(&c.version, "version", false, "Prints program version")
	fs.StringVar(&c.hostname, "hostname", "", "Name for the device this proxy represents")
	fs.StringVar(&c.authKey, "authkey", "", "Tailscale device authkey (defaults to the TS_AUTHKEY environment variable)")
	fs.StringVar(&c.storageDir, "dir", "", "State storage directory. If set, the device is persistent and keeps its identity across restarts. If not, it is ephemeral and removed from the tailnet on shutdown")
	fs.StringVar(&c.controlURL, "control-url", "", "Set if the coordination server is not the default")
	fs.BoolVar(&c.noLogUpload, "no-log-upload", false, "Don't upload tailscale logs to Tailscale. Tailscale can't provide support for devices that opt out")
	fs.TextVar(&c.logLevel, "log-level", slog.LevelInfo, "Log level to use (debug, info, warn, error). Tailscale's internal logs are shown at debug")
}

func (c *serveConfig) createServer(log *slog.Logger) (*tsnet.Server, error) {
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
		// Messages for the user, like the node's state or a login URL.
		UserLogf: slogf(log, slog.LevelInfo),
	}
	// Tailscale's backend logs are verbose, so only show them for debugging.
	// Formatting them is skipped entirely at other levels.
	if log.Enabled(context.Background(), slog.LevelDebug) {
		srv.Logf = slogf(log, slog.LevelDebug)
	}
	if c.noLogUpload {
		envknob.SetNoLogsNoSupport()
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

// slogf adapts logger to the printf-style loggers used by tailscale.
func slogf(log *slog.Logger, level slog.Level) logger.Logf {
	log = log.With("component", "tailscale")
	return func(format string, args ...any) {
		log.Log(context.Background(), level, strings.TrimSpace(fmt.Sprintf(format, args...)))
	}
}

func (c *serveConfig) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: c.logLevel,
	}))
}
