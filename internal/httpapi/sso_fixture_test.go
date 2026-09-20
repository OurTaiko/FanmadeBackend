package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
	"ourtaiko.dev/fanmade/api/internal/database"
)

// Regression tests run the production ID-only schema and real HTTP SSO bridge.
// Legacy fixtures become the fake identity provider's private data, never runtime fallback.
func testServer(t *testing.T, pool *pgxpool.Pool, cfg Config) *Server {
	t.Helper()
	ctx := context.Background()
	_, e := pool.Exec(ctx, `CREATE TABLE test_sso_users AS TABLE users; CREATE TABLE test_old_sessions AS TABLE sessions`)
	if e != nil {
		t.Fatal(e)
	}
	if e = database.MigrateSSO(ctx, pool, func(context.Context, []string) error { return nil }); e != nil {
		t.Fatal(e)
	}
	_, e = pool.Exec(ctx, `ALTER TABLE sessions ALTER COLUMN upstream_token SET DEFAULT ''::bytea`)
	if e != nil {
		t.Fatal(e)
	}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-ID") != "fanmade" || r.Header.Get("X-Service-Key") != strings.Repeat("a", 64) || r.Header.Get("Origin") != "" {
			w.WriteHeader(401)
			return
		}
		var body map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(400)
			return
		}
		str := func(k string) string { var v string; _ = json.Unmarshal(body[k], &v); return v }
		user := func(where string, args ...any) (User, error) {
			var u User
			e := pool.QueryRow(r.Context(), `SELECT id,username,nickname,email_verified_at IS NOT NULL,is_admin FROM test_sso_users WHERE `+where, args...).Scan(&u.ID, &u.Username, &u.Nickname, &u.EmailVerified, &u.IsAdmin)
			return u, e
		}
		switch r.URL.Path {
		case "/internal/v1/game/login":
			u, e := user("username=$1", str("username"))
			var password string
			_ = pool.QueryRow(r.Context(), `SELECT password_hash FROM test_sso_users WHERE id=$1`, u.ID).Scan(&password)
			if e != nil || bcrypt.CompareHashAndPassword([]byte(password), []byte(str("password"))) != nil {
				w.WriteHeader(401)
				return
			}
			token := ID() + ID()
			_, e = pool.Exec(r.Context(), `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,$2,'',now()+interval '1 day')`, hash("game:"+token), u.ID)
			if e != nil {
				w.WriteHeader(503)
				return
			}
			respond(w, 200, map[string]any{"user": u, "accessToken": token, "expiresIn": 86400})
		case "/internal/v1/game/introspect", "/internal/v1/web/introspect":
			var id string
			if strings.Contains(r.URL.Path, "/game/") {
				_ = pool.QueryRow(r.Context(), `SELECT user_id FROM sessions WHERE token_hash=$1 AND expires_at>now()`, hash("game:"+str("accessToken"))).Scan(&id)
			} else {
				v, ok := strings.CutPrefix(str("accessToken"), "web:")
				if ok {
					id = v
				}
			}
			u, e := user("id=$1", id)
			if e != nil {
				w.WriteHeader(401)
				return
			}
			respond(w, 200, map[string]any{"user": u})
		case "/internal/v1/web/revoke":
			respond(w, 200, map[string]bool{"ok": true})
		case "/internal/v1/users/lookup":
			var ids []string
			_ = json.Unmarshal(body["userIds"], &ids)
			users := []map[string]string{}
			for _, id := range ids {
				u, e := user("id=$1", id)
				if e == nil {
					users = append(users, map[string]string{"id": u.ID, "nickname": u.Nickname})
				}
			}
			respond(w, 200, map[string]any{"users": users})
		case "/internal/v1/users/search":
			ids := []string{}
			rows, e := pool.Query(r.Context(), `SELECT id FROM test_sso_users WHERE nickname ILIKE '%'||$1||'%'`, str("query"))
			if e != nil {
				w.WriteHeader(503)
				return
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				_ = rows.Scan(&id)
				ids = append(ids, id)
			}
			respond(w, 200, map[string]any{"userIds": ids, "next": ""})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(remote.Close)
	cfg.SSO, e = NewSSO(SSOConfig{Issuer: remote.URL, ServiceID: "fanmade", ServiceKey: strings.Repeat("a", 64), ClientID: "web", ClientSecret: "secret", RedirectURL: "http://127.0.0.1/callback", EncryptionKey: strings.Repeat("b", 64)})
	if e != nil {
		t.Fatal(e)
	}
	rows, e := pool.Query(ctx, `SELECT token_hash,user_id,csrf_token,expires_at FROM test_old_sessions`)
	if e != nil {
		t.Fatal(e)
	}
	type old struct {
		hash, id, csrf string
		expires        any
	}
	items := []old{}
	for rows.Next() {
		values, e := rows.Values()
		if e != nil {
			t.Fatal(e)
		}
		items = append(items, old{values[0].(string), values[1].(string), values[2].(string), values[3]})
	}
	rows.Close()
	for _, v := range items {
		_, e = pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at,upstream_token) VALUES($1,$2,$3,$4,$5)`, v.hash, v.id, v.csrf, v.expires, cfg.SSO.seal("web:"+v.id, v.hash))
		if e != nil {
			t.Fatal(e)
		}
	}
	return New(pool, cfg)
}
func seedWebSession(t *testing.T, s *Server, id string) string {
	t.Helper()
	token := ID() + ID()
	digest := hash(token)
	_, e := s.DB.Exec(context.Background(), `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at,upstream_token) VALUES($1,$2,'csrf',now()+interval '1 day',$3)`, digest, id, s.Config.SSO.seal("web:"+id, digest))
	if e != nil {
		t.Fatal(e)
	}
	return token
}
