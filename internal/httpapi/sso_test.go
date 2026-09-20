package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ourtaiko.dev/fanmade/api/internal/database"
)

func TestSSOMigrationGuardAndLiveProfile(t *testing.T) {
	pool := scoreTestDB(t)
	ctx := context.Background()
	id := ID()
	_, e := pool.Exec(ctx, `INSERT INTO users(id,username,nickname,password_hash) VALUES($1,'Private123','公开昵称','unused')`, id)
	if e != nil {
		t.Fatal(e)
	}
	denied := errors.New("identity is missing")
	if e = database.MigrateSSO(ctx, pool, func(context.Context, []string) error { return denied }); !errors.Is(e, denied) {
		t.Fatal(e)
	}
	var password string
	if e = pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1`, id).Scan(&password); e != nil || password != "unused" {
		t.Fatal("failed migration destroyed identity", e)
	}
	app := testServer(t, pool, Config{Origin: "http://localhost"})
	handler := app.Handler()
	token := seedWebSession(t, app, id)
	if e = database.MigrateSSO(ctx, pool, nil); e != nil {
		t.Fatal("migration not idempotent", e)
	}
	var columns int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='users'`).Scan(&columns); e != nil || columns != 1 {
		t.Fatal("local profiles retained", columns, e)
	}
	call := func(method, path, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		r.Header.Set("Origin", "http://localhost")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", csrf)
		r.AddCookie(&http.Cookie{Name: "ourtaiko_session", Value: token})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := call("GET", "/api/v1/me", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "公开昵称") {
		t.Fatal(w.Body.String())
	}
	_, e = pool.Exec(ctx, `UPDATE test_sso_users SET nickname='更新昵称',is_admin=true WHERE id=$1`, id)
	if e != nil {
		t.Fatal(e)
	}
	w = call("GET", "/api/v1/me", "")
	if !strings.Contains(w.Body.String(), "更新昵称") || !strings.Contains(w.Body.String(), `"isAdmin":true`) {
		t.Fatal("stale SSO profile", w.Body.String())
	}
	for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/register", "/api/v1/auth/email-code"} {
		if w := call("POST", path, ""); w.Code != 410 {
			t.Fatal(path, w.Code)
		}
	}
	if w := call("PATCH", "/api/v1/me", "csrf"); w.Code != 410 {
		t.Fatal(w.Code)
	}
	if w := call("POST", "/api/v1/auth/logout", "invalid"); w.Code != 403 {
		t.Fatal("CSRF accepted", w.Code)
	}
	// A remote outage cannot authorize a local cached user, but local logout still succeeds.
	app.Config.SSO.http = &http.Client{Transport: failingTransport{}, Timeout: time.Second}
	if w := call("GET", "/api/v1/me", ""); w.Code != 503 {
		t.Fatal("SSO outage bypass", w.Code)
	}
	if w := call("POST", "/api/v1/auth/logout", "csrf"); w.Code != 200 {
		t.Fatal("outage prevented logout", w.Code)
	}
	w = call("GET", "/api/v1/me", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"user":null`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("offline")
}

func TestSSOCallbackBindingAndEncryption(t *testing.T) {
	pool := scoreTestDB(t)
	app := testServer(t, pool, Config{Origin: "http://localhost"})
	h := app.Handler()
	state, binding := ID()+ID(), ID()+ID()
	_, e := pool.Exec(context.Background(), `INSERT INTO oidc_flows VALUES($1,$2,'nonce','verifier','/upload',now()+interval '5 minutes')`, hash(state), hash(binding))
	if e != nil {
		t.Fatal(e)
	}
	call := func(cookie, query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/v1/auth/sso/callback?state="+state+query, nil)
		r.AddCookie(&http.Cookie{Name: "ourtaiko_oidc", Value: cookie})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call(ID()+ID(), "&code=forged"); w.Code != 303 || w.Header().Get("Location") != "http://localhost/login?error=sso" {
		t.Fatal(w.Code)
	}
	var n int
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM oidc_flows`).Scan(&n)
	if n != 1 {
		t.Fatal("mismatched browser consumed flow")
	}
	call(binding, "&error=access_denied")
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM oidc_flows`).Scan(&n)
	if n != 0 {
		t.Fatal("cancelled flow reusable")
	}
	if w := call(binding, "&code=replay"); w.Code != 303 {
		t.Fatal(w.Code)
	}
	c := app.Config.SSO
	encrypted := c.seal("secret", "sessionA")
	if string(encrypted) == "secret" {
		t.Fatal("token not encrypted")
	}
	if _, e := c.unseal(encrypted, "sessionB"); e == nil {
		t.Fatal("ciphertext can be swapped")
	}
	encrypted[len(encrypted)-1] ^= 1
	if _, e := c.unseal(encrypted, "sessionA"); e == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	for _, input := range []string{"https://evil.example", "//evil.example", "/\\evil.example", "/x\r\ny"} {
		if safeReturn(input) != "/upload" {
			t.Fatal(input)
		}
	}
	if safeReturn("/me/charts?page=2") != "/me/charts?page=2" {
		t.Fatal("lost valid return path")
	}
}
func TestPublicNamesDoNotExposeLogin(t *testing.T) {
	pool := scoreTestDB(t)
	id := ID()
	_, e := pool.Exec(context.Background(), `INSERT INTO users(id,username,nickname,password_hash) VALUES($1,'PrivateLogin','公开名','unused')`, id)
	if e != nil {
		t.Fatal(e)
	}
	app := testServer(t, pool, Config{})
	names := app.publicNames(context.Background(), []string{id, id})
	data, _ := json.Marshal(names)
	if strings.Contains(string(data), "PrivateLogin") || names[id] != "公开名" {
		t.Fatal(string(data))
	}
	matches, e := app.Config.SSO.search(context.Background(), "PrivateLogin")
	if e != nil || len(matches) != 0 {
		t.Fatal("login name searchable", e)
	}
	app.Config.SSO.http = &http.Client{Transport: failingTransport{}}
	if app.publicNames(context.Background(), []string{id})[id] != "未知用户" {
		t.Fatal("no outage fallback")
	}
}
