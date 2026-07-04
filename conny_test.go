package conny

import (
	"strings"
	"testing"

	"connectrpc.com/vanguard"
)

func TestResolveTarget(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		wantURL string
		wantH2C bool
		wantErr bool
	}{
		{"http", "http://localhost:8080", "http://localhost:8080", false, false},
		{"https", "https://api.example.com", "https://api.example.com", false, false},
		{"h2c rewritten", "h2c://localhost:8080", "http://localhost:8080", true, false},
		{"empty", "", "", false, true},
		{"invalid", "http://bad url with spaces", "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Config{Target: tt.target}
			u, h2c, err := c.resolveTarget()
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if u.String() != tt.wantURL {
				t.Errorf("url = %q, want %q", u.String(), tt.wantURL)
			}
			if h2c != tt.wantH2C {
				t.Errorf("h2c = %v, want %v", h2c, tt.wantH2C)
			}
		})
	}
}

func TestParseProtocol(t *testing.T) {
	tests := []struct {
		in      string
		want    vanguard.Protocol
		wantErr bool
	}{
		{"", vanguard.ProtocolConnect, false},
		{"connect", vanguard.ProtocolConnect, false},
		{"grpc", vanguard.ProtocolGRPC, false},
		{"grpcweb", vanguard.ProtocolGRPCWeb, false},
		{"grpc-web", vanguard.ProtocolGRPCWeb, false},
		{"http", 0, true},
	}
	for _, tt := range tests {
		got, err := parseProtocol(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseProtocol(%q): expected error, got nil", tt.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseProtocol(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parseProtocol(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestResolveDescriptorRequired(t *testing.T) {
	c := Config{}
	if _, err := c.resolveDescriptor(); err == nil {
		t.Fatal("expected error for missing descriptor, got nil")
	}
}

func TestNewHandlerValidation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"missing descriptor", Config{Target: "http://localhost:8080"}, "Descriptor or DescriptorPath"},
		{"missing target", Config{DescriptorPath: "testdata/nope.pb"}, ""},
		{"bad descriptor path", Config{DescriptorPath: "testdata/nope.pb", Target: "http://localhost:8080"}, "reading descriptor file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewHandler(tt.cfg)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}
