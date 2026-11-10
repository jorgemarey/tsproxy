package main

import (
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseInbound(t *testing.T) {
	tests := []struct {
		spec    string
		want    mapping
		wantErr bool
	}{
		{spec: "127.0.0.1:32400", want: mapping{listen: endpoint{"tcp", ":32400"}, dest: endpoint{"tcp", "127.0.0.1:32400"}}},
		{spec: "80=127.0.0.1:8080", want: mapping{listen: endpoint{"tcp", ":80"}, dest: endpoint{"tcp", "127.0.0.1:8080"}}},
		{spec: "443=127.0.0.1:8080,tls", want: mapping{listen: endpoint{"tcp", ":443"}, dest: endpoint{"tcp", "127.0.0.1:8080"}, tls: true}},
		{spec: "[::1]:8443,tls", want: mapping{listen: endpoint{"tcp", ":8443"}, dest: endpoint{"tcp", "[::1]:8443"}, tls: true}},
		{spec: "22=[fd7a:115c:a1e0::1]:2222", want: mapping{listen: endpoint{"tcp", ":22"}, dest: endpoint{"tcp", "[fd7a:115c:a1e0::1]:2222"}}},
		{spec: "localhost:http", want: mapping{listen: endpoint{"tcp", ":80"}, dest: endpoint{"tcp", "localhost:http"}}},
		{spec: "http=localhost:8080", want: mapping{listen: endpoint{"tcp", ":80"}, dest: endpoint{"tcp", "localhost:8080"}}},
		{spec: "9000=unix:/run/app.sock", want: mapping{listen: endpoint{"tcp", ":9000"}, dest: endpoint{"unix", "/run/app.sock"}}},
		{spec: "443=unix:/run/app.sock,tls", want: mapping{listen: endpoint{"tcp", ":443"}, dest: endpoint{"unix", "/run/app.sock"}, tls: true}},
		{spec: "unix:/run/app.sock", wantErr: true}, // no tailnet port
		{spec: "80=unix:", wantErr: true},
		{spec: "127.0.0.1", wantErr: true},
		{spec: "::1", wantErr: true},
		{spec: "127.0.0.1:", wantErr: true},
		{spec: "127.0.0.1:0", wantErr: true},
		{spec: "127.0.0.1:70000", wantErr: true},
		{spec: "0=127.0.0.1:80", wantErr: true},
		{spec: "70000=127.0.0.1:80", wantErr: true},
		{spec: "abc=127.0.0.1:80", wantErr: true},
		{spec: "", wantErr: true},
	}
	for _, tt := range tests {
		got, err := parseInbound(tt.spec)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseInbound(%q) = %+v, want error", tt.spec, got)
			}
			continue
		}
		tt.want.dir = inbound
		if err != nil || got != tt.want {
			t.Errorf("parseInbound(%q) = %+v, %v, want %+v", tt.spec, got, err, tt.want)
		}
	}
}

func TestParseOutbound(t *testing.T) {
	tests := []struct {
		spec    string
		want    mapping
		wantErr bool
	}{
		{spec: "db:5432", want: mapping{listen: endpoint{"tcp", "127.0.0.1:5432"}, dest: endpoint{"tcp", "db:5432"}}},
		{spec: "15432=db:5432", want: mapping{listen: endpoint{"tcp", "127.0.0.1:15432"}, dest: endpoint{"tcp", "db:5432"}}},
		{spec: "0.0.0.0:5432=db.example.ts.net:5432", want: mapping{listen: endpoint{"tcp", "0.0.0.0:5432"}, dest: endpoint{"tcp", "db.example.ts.net:5432"}}},
		{spec: "[::1]:5432=100.64.0.1:5432", want: mapping{listen: endpoint{"tcp", "[::1]:5432"}, dest: endpoint{"tcp", "100.64.0.1:5432"}}},
		{spec: "unix:/tmp/redis.sock=cache:6379", want: mapping{listen: endpoint{"unix", "/tmp/redis.sock"}, dest: endpoint{"tcp", "cache:6379"}}},
		{spec: "db:5432,tls", wantErr: true},
		{spec: "5432=unix:/tmp/x.sock", wantErr: true}, // tailnet has no unix sockets
		{spec: "abc=db:5432", wantErr: true},
		{spec: "0=db:5432", wantErr: true},
		{spec: "db", wantErr: true},
	}
	for _, tt := range tests {
		got, err := parseOutbound(tt.spec)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseOutbound(%q) = %+v, want error", tt.spec, got)
			}
			continue
		}
		tt.want.dir = outbound
		if err != nil || got != tt.want {
			t.Errorf("parseOutbound(%q) = %+v, %v, want %+v", tt.spec, got, err, tt.want)
		}
	}
}

func TestParseMappingsDuplicates(t *testing.T) {
	tests := []struct {
		name         string
		in, out      []string
		wantErr      bool
		wantMappings int
	}{
		{name: "distinct", in: []string{"80=127.0.0.1:8080", "443=127.0.0.1:8080,tls"}, out: []string{"db:5432"}, wantMappings: 3},
		{name: "same tailnet and local port", in: []string{"127.0.0.1:5432"}, out: []string{"db:5432"}, wantMappings: 2},
		{name: "same tailnet port", in: []string{"80=127.0.0.1:8080", "127.0.0.1:80"}, wantErr: true},
		{name: "tls and plain on one port", in: []string{"443=127.0.0.1:8080", "443=127.0.0.1:8443,tls"}, wantErr: true},
		{name: "same local port", out: []string{"db:5432", "5432=other:5432"}, wantErr: true},
		{name: "same local socket", out: []string{"unix:/tmp/a.sock=db:5432", "unix:/tmp/a.sock=other:6379"}, wantErr: true},
	}
	for _, tt := range tests {
		got, err := parseMappings(tt.in, tt.out)
		if tt.wantErr != (err != nil) || len(got) != tt.wantMappings {
			t.Errorf("%s: parseMappings() = %d mappings, %v; want %d mappings, error %v", tt.name, len(got), err, tt.wantMappings, tt.wantErr)
		}
	}
}

// TestUnixSocketProxy forwards a unix socket listener to a unix socket
// destination, covering both sides of unix socket support.
func TestUnixSocketProxy(t *testing.T) {
	dir := t.TempDir()
	dstPath := filepath.Join(dir, "dst.sock")
	dst, err := net.Listen("unix", dstPath)
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
		io.Copy(c, c) // echo until the client half-closes
	}()

	frontPath := filepath.Join(dir, "front.sock")
	front, err := listenUnix(frontPath)
	if err != nil {
		t.Fatal(err)
	}
	defer front.Close()
	m := mapping{dir: inbound, dest: endpoint{"unix", dstPath}}
	go serve(slog.New(slog.DiscardHandler), front, dialer(nil, m), &connTracker{})

	client, err := net.Dial("unix", frontPath)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(client, "hello")
	client.(*net.UnixConn).CloseWrite()
	got, err := io.ReadAll(client)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q, want %q", got, "hello")
	}
}

func TestListenUnixStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.sock")

	// A live socket must not be replaced.
	live, err := listenUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := listenUnix(path); err == nil {
		t.Fatal("listenUnix succeeded on a socket that is in use")
	}

	// Simulate a crash: the socket file stays but nobody listens on it.
	live.(*net.UnixListener).SetUnlinkOnClose(false)
	live.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("socket file should still exist: %v", err)
	}
	ln, err := listenUnix(path)
	if err != nil {
		t.Fatalf("listenUnix did not replace the stale socket: %v", err)
	}
	ln.Close()
}
