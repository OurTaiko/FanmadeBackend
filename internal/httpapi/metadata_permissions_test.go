package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetadataAuthorOrAdmin(t *testing.T) {
	pool := scoreTestDB(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `INSERT INTO users(id,username,password_hash,is_admin) VALUES('d46774d30dd13b92d9e536808da468a4','author','unused',false),('9b893bc6d9422c93536ff0df503b81e9','another','unused',false),('5f63f79d876d201a13f8710453636837','adminuser','unused',true);`)
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{"d46774d30dd13b92d9e536808da468a4": strings.Repeat("1", 64), "9b893bc6d9422c93536ff0df503b81e9": strings.Repeat("2", 64), "5f63f79d876d201a13f8710453636837": strings.Repeat("3", 64)}
	for id, token := range tokens {
		if _, err = pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,$2,'csrf',now()+interval '1 day')`, hash(token), id); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO files(id,storage_key,original_filename,sha256,byte_size,media_type) VALUES('t','t','t.tja',repeat('a',64),1,'application/octet-stream'),('a','a','a.ogg',repeat('b',64),1,'audio/ogg');
 INSERT INTO charts(id,owner_id,current_version_id) VALUES('chart','d46774d30dd13b92d9e536808da468a4','v');
 INSERT INTO chart_versions(id,chart_id,version_number,title,bpm,duration,encoding,wave_filename,tja_file_id,audio_file_id,validation_version) VALUES('v','chart',1,'Original',120,10,'utf-8','a.ogg','t','a','test');`)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	handler := testServer(t, pool, Config{Origin: "http://127.0.0.1:5173"}).Handler()
	call := func(actor, csrf, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PATCH", "/api/v1/charts/chart", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://127.0.0.1:5173")
		r.Header.Set("X-CSRF-Token", csrf)
		if actor != "" {
			r.AddCookie(&http.Cookie{Name: "ourtaiko_session", Value: tokens[actor]})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		actor, csrf, body string
		status            int
	}{
		{"", "csrf", `{"title":"Anonymous"}`, 401},
		{"9b893bc6d9422c93536ff0df503b81e9", "csrf", `{"title":"Denied"}`, 403},
		{"5f63f79d876d201a13f8710453636837", "", `{"title":"Missing CSRF"}`, 403},
		{"9b893bc6d9422c93536ff0df503b81e9", "csrf", `{"title":"Forged","isAdmin":true}`, 400},
		{"d46774d30dd13b92d9e536808da468a4", "csrf", `{"title":"Author edit"}`, 200},
		{"5f63f79d876d201a13f8710453636837", "csrf", `{"title":"Admin edit"}`, 200},
	} {
		w := call(tc.actor, tc.csrf, tc.body)
		if w.Code != tc.status {
			t.Fatalf("actor %s wanted %d: %d %s", tc.actor, tc.status, w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest("GET", "/api/v1/me", nil)
	r.AddCookie(&http.Cookie{Name: "ourtaiko_session", Value: tokens["5f63f79d876d201a13f8710453636837"]})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	var me struct{ User User }
	if json.Unmarshal(w.Body.Bytes(), &me) != nil || !me.User.IsAdmin {
		t.Fatalf("missing admin role: %s", w.Body.String())
	}
	if _, err = pool.Exec(ctx, `UPDATE test_sso_users SET is_admin=false WHERE id='5f63f79d876d201a13f8710453636837'`); err != nil {
		t.Fatal(err)
	}
	w = call("5f63f79d876d201a13f8710453636837", "csrf", `{"title":"Revoked"}`)
	if w.Code != 403 {
		t.Fatalf("old session kept admin: %s", w.Body.String())
	}
	var title string
	if err = pool.QueryRow(ctx, `SELECT title_override FROM charts WHERE id='chart'`).Scan(&title); err != nil || title != "Admin edit" {
		t.Fatalf("denied write changed title: %s %v", title, err)
	}
	// Registration does not accept an administrator flag.
	r = httptest.NewRequest("POST", "/api/v1/auth/register", strings.NewReader(`{"username":"forgedadmin","password":"password123","isAdmin":true}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://127.0.0.1:5173")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 410 {
		t.Fatalf("registration accepted role: %s", w.Body.String())
	}
}
