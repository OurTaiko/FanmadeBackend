package httpapi

import (
	"strings"
	"testing"
)

func TestSSOApplicationCredentials(t *testing.T) {
	cfg := SSOConfig{Issuer: "http://127.0.0.1:8090", RedirectURL: "http://127.0.0.1:8080/callback",
		ServiceID: strings.Repeat("C", 40), ServiceKey: strings.Repeat("Opaque-Application_Secret", 3),
		ClientID: "website", ClientSecret: "website-secret", EncryptionKey: strings.Repeat("a", 64)}
	if _, err := NewSSO(cfg); err != nil {
		t.Fatal("canonical Application credentials rejected", err)
	}
	for _, bad := range []string{"", "short", strings.Repeat("a", 1025), strings.Repeat("a", 40) + "\r\nX-Other: injected", strings.Repeat("a", 40) + " "} {
		invalid := cfg
		invalid.ServiceKey = bad
		if _, err := NewSSO(invalid); err == nil {
			t.Fatal("invalid service secret accepted")
		}
	}
	cfg.ServiceID = strings.Repeat("x", 101)
	if _, err := NewSSO(cfg); err == nil {
		t.Fatal("oversized Application ID accepted")
	}
}
