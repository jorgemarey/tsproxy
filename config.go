package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"tailscale.com/envknob"
	"tailscale.com/ipn/store/mem"
	"tailscale.com/tsnet"
	"tailscale.com/types/logger"
)

const usage = `Usage: tsproxy [options] [MAPPING...]

Exposes local services on the tailnet (-tailnet) and tailnet services
locally (-local). Both options can be repeated, and are named after where
the mapping listens. MAPPING arguments are the same as -tailnet. At least
one mapping is required.

-tailnet, listen on the tailnet and connect to a local service:
[PORT=]DEST[,tls]
  DEST is HOST:PORT or unix:/path/to.sock. PORT is the port on the tailnet;
  it defaults to DEST's port and is required for unix sockets. With ",tls"
  the port serves TLS using the device's Tailscale certificate (HTTPS and
  MagicDNS must be enabled in the tailnet).

-local, listen locally and connect to a tailnet service:
[LISTEN=]HOST:PORT
  HOST:PORT is an address on the tailnet. LISTEN is a PORT (bound to
  127.0.0.1), HOST:PORT or unix:/path/to.sock; it defaults to 127.0.0.1
  and the tailnet port.

Examples:
  tsproxy -hostname plex 127.0.0.1:32400
  tsproxy -hostname web -tailnet 80=127.0.0.1:8080 -tailnet 443=127.0.0.1:8080,tls
  tsproxy -hostname app -tailnet 9000=unix:/run/app.sock -local 5432=db:5432 -local unix:/tmp/redis.sock=cache:6379

Options:
`

// tailnetSpecs returns the specs of the mappings listening on the tailnet:
// the -tailnet options followed by the MAPPING arguments.
func (c *serveConfig) tailnetSpecs(args []string) []string {
	return slices.Concat([]string(c.tailnet), args)
}

// webUIPort is the tailnet port of the Tailscale web interface (-webui).
const webUIPort = 5252

// stringList is a flag.Value collecting every use of a repeatable flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ", ") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

type serveConfig struct {
	version     bool
	hostname    string
	authKey     string
	authKeyFile string
	storageDir  string
	controlURL  string
	noLogUpload bool
	upTimeout   time.Duration

	shutdownTimeout time.Duration
	healthAddr      string
	webUI           bool
	tags            string
	logLevel        slog.Level
	tailnet         stringList
	local           stringList

	// tempDir is the temporary tsnet directory used by ephemeral nodes. It is
	// removed by cleanup.
	tempDir string
}

func (c *serveConfig) configureByFlags(fs *flag.FlagSet) {
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), usage)
		fs.PrintDefaults()
	}
	fs.Var(&c.tailnet, "tailnet", "Expose a local service on the tailnet: [PORT=]DEST[,tls]. Can be repeated")
	fs.Var(&c.local, "local", "Expose a tailnet service locally: [LISTEN=]HOST:PORT. Can be repeated")
	fs.BoolVar(&c.version, "version", false, "Prints program version")
	fs.StringVar(&c.hostname, "hostname", "", "Name for the device this proxy represents")
	fs.StringVar(&c.authKey, "authkey", "", "Tailscale device authkey (defaults to the TS_AUTHKEY environment variable)")
	fs.StringVar(&c.authKeyFile, "authkey-file", "", "File containing the Tailscale device authkey. Avoids exposing the key in the process arguments or environment")
	fs.StringVar(&c.tags, "tags", "", "Comma-separated tags to advertise for the device, e.g. tag:proxy,tag:web. Required when the authkey is an OAuth client secret")
	fs.StringVar(&c.storageDir, "dir", "", "State storage directory. If set, the device is persistent and keeps its identity across restarts. If not, it is ephemeral and removed from the tailnet on shutdown")
	fs.StringVar(&c.controlURL, "control-url", "", "Set if the coordination server is not the default")
	fs.DurationVar(&c.upTimeout, "up-timeout", time.Minute, "How long to wait for the device to connect to the tailnet before exiting. 0 waits forever")
	fs.DurationVar(&c.shutdownTimeout, "shutdown-timeout", 5*time.Second, "On shutdown, how long to wait for open connections to finish before closing them. A second signal exits immediately")
	fs.StringVar(&c.healthAddr, "health-addr", "", "Address to serve HTTP health checks on, e.g. 127.0.0.1:9002: /healthz (process is up) and /readyz (proxy is listening and connected to the tailnet). Disabled if empty")
	fs.BoolVar(&c.webUI, "webui", false, fmt.Sprintf("Run the Tailscale web interface for this device on tailnet port %d", webUIPort))
	fs.BoolVar(&c.noLogUpload, "no-log-upload", false, "Don't upload tailscale logs to Tailscale. Tailscale can't provide support for devices that opt out")
	fs.TextVar(&c.logLevel, "log-level", slog.LevelInfo, "Log level to use (debug, info, warn, error). Tailscale's internal logs are shown at debug")
}

func (c *serveConfig) createServer(log *slog.Logger) (*tsnet.Server, error) {
	authKey, err := c.resolveAuthKey()
	if err != nil {
		return nil, err
	}
	tags, err := parseTags(c.tags)
	if err != nil {
		return nil, err
	}
	if len(tags) == 0 && strings.HasPrefix(authKey, oauthSecretPrefix) {
		return nil, errors.New("-tags is required when the authkey is an OAuth client secret")
	}

	srv := &tsnet.Server{
		Hostname:   c.hostname,
		AuthKey:    authKey,
		Dir:        c.storageDir,
		ControlURL: c.controlURL,
		Ephemeral:  c.ephemeral(),

		RunWebClient:  c.webUI,
		AdvertiseTags: tags,
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

// reservedPorts returns the tailnet ports used by the device itself, which
// mappings can't listen on.
func (c *serveConfig) reservedPorts() map[int]string {
	if c.webUI {
		return map[int]string{webUIPort: "-webui"}
	}
	return nil
}

// oauthSecretPrefix starts Tailscale OAuth client secrets. tsnet exchanges
// them for a new auth key on every start, which requires tags.
const oauthSecretPrefix = "tskey-client-"

// parseTags parses a comma-separated list of tags like "tag:a, tag:b".
func parseTags(s string) ([]string, error) {
	var tags []string
	for t := range strings.SplitSeq(s, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		name, ok := strings.CutPrefix(t, "tag:")
		if !ok || name == "" {
			return nil, fmt.Errorf("invalid tag %q: tags must look like tag:name", t)
		}
		if !slices.Contains(tags, t) {
			tags = append(tags, t)
		}
	}
	return tags, nil
}

// resolveAuthKey returns the authkey from -authkey, -authkey-file or the
// TS_AUTHKEY environment variable, in that order. The environment variable is
// read here rather than used as the flag default so the key is never printed
// by the usage output.
func (c *serveConfig) resolveAuthKey() (string, error) {
	if c.authKey != "" && c.authKeyFile != "" {
		return "", errors.New("only one of -authkey and -authkey-file can be set")
	}
	key := c.authKey
	if c.authKeyFile != "" {
		b, err := os.ReadFile(c.authKeyFile)
		if err != nil {
			return "", fmt.Errorf("reading authkey file: %w", err)
		}
		key = strings.TrimSpace(string(b))
		if key == "" {
			return "", fmt.Errorf("authkey file %s is empty", c.authKeyFile)
		}
	}
	if key == "" {
		key = os.Getenv("TS_AUTHKEY")
	}
	if key == "" {
		return "", errors.New("an authkey must be provided with -authkey, -authkey-file or TS_AUTHKEY")
	}
	return key, nil
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
