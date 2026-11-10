package main

import "testing"

func TestServePort(t *testing.T) {
	tests := []struct {
		destination string
		flagPort    int
		want        int
		wantErr     bool
	}{
		{destination: "127.0.0.1:32400", want: 32400},
		{destination: "[::1]:8443", want: 8443},
		{destination: "[fd7a:115c:a1e0::1]:22", want: 22},
		{destination: "localhost:http", want: 80},
		{destination: "127.0.0.1:32400", flagPort: 80, want: 80},
		{destination: "[::1]:8443", flagPort: 443, want: 443},
		{destination: "127.0.0.1", wantErr: true},
		{destination: "::1", wantErr: true},
		{destination: "127.0.0.1:", wantErr: true},
		{destination: "127.0.0.1:0", wantErr: true},
		{destination: "127.0.0.1:70000", wantErr: true},
		{destination: "127.0.0.1:notaport", wantErr: true},
		{destination: "127.0.0.1:80", flagPort: -1, wantErr: true},
		{destination: "127.0.0.1:80", flagPort: 70000, wantErr: true},
	}
	for _, tt := range tests {
		cfg := &serveConfig{port: tt.flagPort}
		got, err := cfg.servePort(tt.destination)
		if tt.wantErr {
			if err == nil {
				t.Errorf("servePort(%q) with -port %d = %d, want error", tt.destination, tt.flagPort, got)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("servePort(%q) with -port %d = %d, %v, want %d", tt.destination, tt.flagPort, got, err, tt.want)
		}
	}
}
