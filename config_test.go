package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestResolveAuthKey(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key")
	if err := os.WriteFile(keyFile, []byte("tskey-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	emptyFile := filepath.Join(dir, "empty")
	if err := os.WriteFile(emptyFile, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		cfg     serveConfig
		env     string
		want    string
		wantErr bool
	}{
		{name: "flag", cfg: serveConfig{authKey: "tskey-flag"}, env: "tskey-env", want: "tskey-flag"},
		{name: "file, trimmed", cfg: serveConfig{authKeyFile: keyFile}, env: "tskey-env", want: "tskey-file"},
		{name: "env", env: "tskey-env", want: "tskey-env"},
		{name: "none", wantErr: true},
		{name: "flag and file", cfg: serveConfig{authKey: "tskey-flag", authKeyFile: keyFile}, wantErr: true},
		{name: "missing file", cfg: serveConfig{authKeyFile: filepath.Join(dir, "missing")}, wantErr: true},
		{name: "empty file", cfg: serveConfig{authKeyFile: emptyFile}, env: "tskey-env", wantErr: true},
	}
	for _, tt := range tests {
		t.Setenv("TS_AUTHKEY", tt.env)
		got, err := tt.cfg.resolveAuthKey()
		if tt.wantErr != (err != nil) || got != tt.want {
			t.Errorf("%s: resolveAuthKey() = %q, %v; want %q, error %v", tt.name, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestParseTags(t *testing.T) {
	tests := []struct {
		in      string
		want    []string
		wantErr bool
	}{
		{in: "", want: nil},
		{in: "tag:proxy", want: []string{"tag:proxy"}},
		{in: " tag:proxy , tag:web,", want: []string{"tag:proxy", "tag:web"}},
		{in: "tag:web,tag:web", want: []string{"tag:web"}},
		{in: "proxy", wantErr: true},
		{in: "tag:", wantErr: true},
		{in: "tag:a,web", wantErr: true},
	}
	for _, tt := range tests {
		got, err := parseTags(tt.in)
		if tt.wantErr != (err != nil) || !slices.Equal(got, tt.want) {
			t.Errorf("parseTags(%q) = %q, %v; want %q, error %v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestCreateServerRequiresTagsForOAuth(t *testing.T) {
	cfg := &serveConfig{authKey: "tskey-client-secret", storageDir: t.TempDir()}
	if _, err := cfg.createServer(slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("createServer succeeded with an OAuth secret and no tags")
	}
	cfg.tags = "tag:proxy"
	srv, err := cfg.createServer(slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("createServer with an OAuth secret and tags: %v", err)
	}
	if !slices.Equal(srv.AdvertiseTags, []string{"tag:proxy"}) {
		t.Fatalf("AdvertiseTags = %q, want [tag:proxy]", srv.AdvertiseTags)
	}
}
