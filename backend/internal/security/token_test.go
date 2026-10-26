package security

import (
	"strings"
	"testing"
)

func TestParseBearerToken(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
		ok     bool
	}{
		{name: "standard bearer", header: "Bearer sk-test", want: "sk-test", ok: true},
		{name: "case insensitive scheme", header: "bearer sk-test", want: "sk-test", ok: true},
		{name: "missing scheme", header: "sk-test", ok: false},
		{name: "missing token", header: "Bearer", ok: false},
		{name: "extra field", header: "Bearer sk-test extra", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseBearerToken(tt.header)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("ParseBearerToken(%q) = %q, %v; want %q, %v", tt.header, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestGenerateTokenIsRandomAndHashable(t *testing.T) {
	first, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}
	second, err := GenerateToken()
	if err != nil {
		t.Fatalf("second GenerateToken() error = %v", err)
	}
	if first == second {
		t.Fatal("GenerateToken returned the same token twice")
	}
	if !strings.HasPrefix(first, "sk-") {
		t.Fatalf("token prefix = %q; want sk-", first)
	}
	if len(first) != len("sk-")+64 {
		t.Fatalf("token length = %d; want %d", len(first), len("sk-")+64)
	}

	hash := HashToken(first)
	if len(hash) != 64 {
		t.Fatalf("hash length = %d; want 64", len(hash))
	}
	if HashToken(first) != hash {
		t.Fatal("HashToken is not deterministic")
	}
}
