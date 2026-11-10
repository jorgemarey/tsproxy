package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"tailscale.com/tsnet"
)

const (
	unixPrefix = "unix:"
	tlsSuffix  = ",tls"
)

// direction says which side of a mapping is on the tailnet.
type direction int

const (
	// inbound listens on the tailnet and dials a local destination.
	inbound direction = iota
	// outbound listens locally and dials a destination on the tailnet.
	outbound
)

// endpoint is a TCP address or a unix socket path.
type endpoint struct {
	network string // "tcp" or "unix"
	address string
}

func (e endpoint) String() string {
	if e.network == "unix" {
		return unixPrefix + e.address
	}
	return e.address
}

// mapping is a single listener and the destination its connections are
// forwarded to.
type mapping struct {
	dir    direction
	listen endpoint
	dest   endpoint
	tls    bool
}

func (m mapping) String() string {
	switch {
	case m.dir == outbound:
		return fmt.Sprintf("%s -> tailnet %s", m.listen, m.dest)
	case m.tls:
		return fmt.Sprintf("tailnet %s (tls) -> %s", m.listen, m.dest)
	default:
		return fmt.Sprintf("tailnet %s -> %s", m.listen, m.dest)
	}
}

// parsePort parses a port number or service name, rejecting port 0.
func parsePort(s string) (int, error) {
	port, err := net.LookupPort("tcp", s)
	if err != nil || port == 0 {
		return 0, fmt.Errorf("invalid port %q", s)
	}
	return port, nil
}

// parseEndpoint parses HOST:PORT (IPv6 hosts in brackets) or unix:/path.
func parseEndpoint(s string) (endpoint, error) {
	if path, ok := strings.CutPrefix(s, unixPrefix); ok {
		if path == "" {
			return endpoint{}, fmt.Errorf("invalid address %q: empty unix socket path", s)
		}
		return endpoint{network: "unix", address: path}, nil
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return endpoint{}, fmt.Errorf("invalid address %q, expected HOST:PORT or unix:/path: %w", s, err)
	}
	if _, err := parsePort(port); err != nil {
		return endpoint{}, fmt.Errorf("invalid address %q: %w", s, err)
	}
	return endpoint{network: "tcp", address: net.JoinHostPort(host, port)}, nil
}

// endpointPort returns the port of a TCP endpoint.
func endpointPort(e endpoint) int {
	_, port, _ := net.SplitHostPort(e.address)
	p, _ := parsePort(port)
	return p
}

// parseInbound parses [PORT=]DEST[,tls], where PORT is the tailnet port and
// DEST is HOST:PORT or unix:/path. PORT defaults to DEST's port.
func parseInbound(s string) (mapping, error) {
	m := mapping{dir: inbound}
	spec := s
	if rest, ok := strings.CutSuffix(spec, tlsSuffix); ok {
		m.tls = true
		spec = rest
	}

	listenPort, dest, hasPort := strings.Cut(spec, "=")
	if !hasPort {
		dest = spec
	}
	var err error
	if m.dest, err = parseEndpoint(dest); err != nil {
		return mapping{}, fmt.Errorf("mapping %q: %w", s, err)
	}

	var port int
	switch {
	case hasPort:
		if port, err = parsePort(listenPort); err != nil {
			return mapping{}, fmt.Errorf("mapping %q: invalid tailnet port: %w", s, err)
		}
	case m.dest.network == "unix":
		return mapping{}, fmt.Errorf("mapping %q: a tailnet port is required for unix sockets, e.g. 80=%s", s, dest)
	default:
		port = endpointPort(m.dest)
	}
	m.listen = endpoint{network: "tcp", address: ":" + strconv.Itoa(port)}
	return m, nil
}

// parseOutbound parses [LISTEN=]HOST:PORT, where HOST:PORT is a tailnet
// address and LISTEN is a local PORT (bound to 127.0.0.1), HOST:PORT or
// unix:/path. LISTEN defaults to 127.0.0.1 and the tailnet port.
func parseOutbound(s string) (mapping, error) {
	if strings.HasSuffix(s, tlsSuffix) {
		return mapping{}, fmt.Errorf("local mapping %q: %s is only supported when listening on the tailnet", s, tlsSuffix)
	}
	m := mapping{dir: outbound}

	listen, dest, hasListen := strings.Cut(s, "=")
	if !hasListen {
		dest = s
	}
	var err error
	if m.dest, err = parseEndpoint(dest); err != nil {
		return mapping{}, fmt.Errorf("local mapping %q: %w", s, err)
	}
	if m.dest.network != "tcp" {
		return mapping{}, fmt.Errorf("local mapping %q: the destination must be a tailnet HOST:PORT", s)
	}

	switch {
	case !hasListen:
		m.listen = endpoint{network: "tcp", address: net.JoinHostPort("127.0.0.1", strconv.Itoa(endpointPort(m.dest)))}
	case !strings.HasPrefix(listen, unixPrefix) && !strings.Contains(listen, ":"):
		port, err := parsePort(listen)
		if err != nil {
			return mapping{}, fmt.Errorf("local mapping %q: invalid listen port: %w", s, err)
		}
		m.listen = endpoint{network: "tcp", address: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	default:
		if m.listen, err = parseEndpoint(listen); err != nil {
			return mapping{}, fmt.Errorf("local mapping %q: %w", s, err)
		}
	}
	return m, nil
}

// parseMappings parses inbound and outbound mapping specs and checks that no
// two of them listen on the same address. reservedPorts are tailnet ports
// already in use by the device itself.
func parseMappings(inboundSpecs, outboundSpecs []string, reservedPorts map[int]string) ([]mapping, error) {
	var mappings []mapping
	for _, s := range inboundSpecs {
		m, err := parseInbound(s)
		if err != nil {
			return nil, err
		}
		mappings = append(mappings, m)
	}
	for _, s := range outboundSpecs {
		m, err := parseOutbound(s)
		if err != nil {
			return nil, err
		}
		mappings = append(mappings, m)
	}

	seen := map[string]mapping{}
	for _, m := range mappings {
		if m.dir == inbound {
			if owner, ok := reservedPorts[endpointPort(m.listen)]; ok {
				return nil, fmt.Errorf("mapping %q uses tailnet port %d, which is used by %s", m, endpointPort(m.listen), owner)
			}
		}
		key := fmt.Sprintf("%d %s", m.dir, m.listen)
		if prev, ok := seen[key]; ok {
			return nil, fmt.Errorf("mappings %q and %q listen on the same address", prev, m)
		}
		seen[key] = m
	}
	return mappings, nil
}

// listen opens the listener for m.
func listen(srv *tsnet.Server, m mapping) (net.Listener, error) {
	switch {
	case m.dir == inbound && m.tls:
		return srv.ListenTLS("tcp", m.listen.address)
	case m.dir == inbound:
		return srv.Listen("tcp", m.listen.address)
	case m.listen.network == "unix":
		return listenUnix(m.listen.address)
	default:
		return net.Listen("tcp", m.listen.address)
	}
}

// listenUnix listens on a unix socket, removing a stale socket file left by a
// previous run. A socket that still accepts connections is left alone.
func listenUnix(path string) (net.Listener, error) {
	if fi, err := os.Stat(path); err == nil && fi.Mode()&os.ModeSocket != 0 {
		if c, err := net.Dial("unix", path); err == nil {
			c.Close()
			return nil, fmt.Errorf("unix socket %s is already in use", path)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("removing stale unix socket %s: %w", path, err)
		}
	}
	return net.Listen("unix", path)
}

// dialFunc connects to a mapping's destination.
type dialFunc func(ctx context.Context) (net.Conn, error)

// dialer returns the function connecting to m's destination: locally for
// inbound mappings and through the tailnet for outbound ones.
func dialer(srv *tsnet.Server, m mapping) dialFunc {
	if m.dir == outbound {
		return func(ctx context.Context) (net.Conn, error) {
			return srv.Dial(ctx, m.dest.network, m.dest.address)
		}
	}
	var d net.Dialer
	return func(ctx context.Context) (net.Conn, error) {
		return d.DialContext(ctx, m.dest.network, m.dest.address)
	}
}
