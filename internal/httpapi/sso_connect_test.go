package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSSOConnectAddressPreservesPublicIssuer(t *testing.T) {
	const issuer = "http://127.0.0.1:1"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "127.0.0.1:1" {
			t.Errorf("public Host was changed: %s", r.Host)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_, _ = w.Write([]byte(`{"issuer":"` + issuer + `","authorization_endpoint":"` + issuer + `/o/authorize/","token_endpoint":"` + issuer + `/o/token/","jwks_uri":"` + issuer + `/jwks"}`))
		case "/internal/v1/users/lookup":
			_, _ = w.Write([]byte(`{"users":[{"id":"11111111111111111111111111111111","nickname":"Docker"}]}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	cfg := SSOConfig{Issuer: issuer, ConnectAddress: strings.TrimPrefix(server.URL, "http://"), ServiceID: "fanmade", ServiceKey: strings.Repeat("a", 64), ClientID: "web", ClientSecret: "secret", RedirectURL: "http://127.0.0.1/callback", EncryptionKey: strings.Repeat("b", 64)}
	c, err := NewSSO(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, oauth, err := c.oidc(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if oauth.Endpoint.AuthURL != issuer+"/o/authorize/" {
		t.Fatal("browser URL changed")
	}
	profiles, err := c.Profiles(context.Background(), []string{"11111111111111111111111111111111"})
	if err != nil || profiles["11111111111111111111111111111111"].Nickname != "Docker" {
		t.Fatal(profiles, err)
	}
	// Unrelated origins must not be routed to the account center.
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer other.Close()
	res, err := c.http.Get(other.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 204 {
		t.Fatal("unrelated origin was rerouted")
	}
	cfg.ConnectAddress = "missing-port"
	if _, err := NewSSO(cfg); err == nil {
		t.Fatal("invalid TCP address accepted")
	}
}

func TestSSOConnectAddressDoesNotBypassTLS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted TLS accepted") }))
	defer server.Close()
	c, err := NewSSO(SSOConfig{Issuer: "https://sso.example.test", ConnectAddress: strings.TrimPrefix(server.URL, "https://"), ServiceID: "fanmade", ServiceKey: strings.Repeat("a", 64), ClientID: "web", ClientSecret: "secret", RedirectURL: "https://fanmade.example.test/callback", EncryptionKey: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.http.Get("https://sso.example.test"); err == nil {
		t.Fatal("TLS verification bypassed")
	}
}
