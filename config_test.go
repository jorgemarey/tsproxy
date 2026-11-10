package main

import (
	"os"
	"path/filepath"
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
