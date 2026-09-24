package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"ourtaiko.dev/fanmade/api/internal/database"
)

func TestUserOIDCLoginAndSessionAreAtomic(t *testing.T) {
	pool := scoreTestDB(t)
	ctx := context.Background()
	if err := database.MigrateSSO(ctx, pool, nil); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", "test-key"))
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			respond(w, 200, map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/keys":
			respond(w, 200, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test-key", Algorithm: "RS256", Use: "sig"}}})
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if r.Form.Get("code_verifier") != "test-verifier" {
				t.Error("PKCE verifier lost")
				w.WriteHeader(400)
				return
			}
			id := r.Form.Get("code")
			claims, _ := json.Marshal(map[string]any{"iss": issuer, "sub": id, "aud": "web", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": "test-nonce"})
			signed, err := signer.Sign(claims)
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			token, err := signed.CompactSerialize()
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			respond(w, 200, map[string]any{"access_token": "web:" + id, "token_type": "Bearer", "expires_in": 3600, "id_token": token})
		case "/internal/v1/web/introspect":
			var body struct {
				Token string `json:"accessToken"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			respond(w, 200, map[string]any{"user": User{ID: strings.TrimPrefix(body.Token, "web:"), Nickname: "公開名"}})
		case "/internal/v1/web/revoke":
			respond(w, 200, map[string]bool{"ok": true})
		default:
			w.WriteHeader(404)
		}
	}))
	defer remote.Close()
	issuer = remote.URL
	client, err := NewSSO(SSOConfig{Issuer: issuer, ServiceID: "fanmade", ServiceKey: strings.Repeat("a", 64), ClientID: "web", ClientSecret: "test-secret", RedirectURL: "http://127.0.0.1/callback", EncryptionKey: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	app := New(pool, Config{Origin: "http://localhost", SSO: client})
	callback := func(id string) *httptest.ResponseRecorder {
		t.Helper()
		state, binding := ID()+ID(), ID()+ID()
		if _, err := pool.Exec(ctx, `INSERT INTO oidc_flows VALUES($1,$2,'test-nonce','test-verifier','/users',now()+interval '5 minutes')`, hash(state), hash(binding)); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("GET", "/api/v1/auth/sso/callback?state="+state+"&code="+id, nil)
		req.AddCookie(&http.Cookie{Name: "ourtaiko_oidc", Value: binding})
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, req)
		return response
	}
	id := ID()
	w := callback(id)
	if w.Code != 303 || w.Header().Get("Location") != "http://localhost/users" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	var first, last time.Time
	if err = pool.QueryRow(ctx, `SELECT first_login_at,last_active_at FROM users WHERE id=$1`, id).Scan(&first, &last); err != nil || last.Before(first) {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id=$1`, id).Scan(&count); err != nil || count != 1 {
		t.Fatal("missing committed web session", count, err)
	}
	// A failed session INSERT must roll back the new user and both activity times.
	if _, err = pool.Exec(ctx, `CREATE FUNCTION fail_test_session() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test session insert failure'; END $$;
 CREATE TRIGGER fail_test_session BEFORE INSERT ON sessions FOR EACH ROW EXECUTE FUNCTION fail_test_session()`); err != nil {
		t.Fatal(err)
	}
	failedID := ID()
	w = callback(failedID)
	if w.Header().Get("Location") != "http://localhost/login?error=sso" {
		t.Fatal(w.Header())
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE id=$1`, failedID).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed callback recorded a login", count, err)
	}
}
