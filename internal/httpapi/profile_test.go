package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
	"ourtaiko.dev/fanmade/api/internal/database"
)

func TestNicknameProfileAndPrivacy(t *testing.T) {
	pool := scoreTestDB(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Recreate the pre-016 user table in this disposable schema to test legacy migration.
	exec(`DROP TRIGGER users_prepare_profile ON users; DROP FUNCTION prepare_user_profile(); ALTER TABLE users DROP COLUMN nickname; DELETE FROM schema_migrations WHERE version=16`)
	password, _ := bcrypt.GenerateFromPassword([]byte("test-password"), bcrypt.MinCost)
	exec(`INSERT INTO users(id,username,password_hash) VALUES('owner','private_owner',$1)`, string(password))
	if err := database.Migrate(ctx, pool, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, pool, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	var nickname string
	if err := pool.QueryRow(ctx, `SELECT nickname FROM users WHERE id='owner'`).Scan(&nickname); err != nil || nickname != "private_owner" {
		t.Fatal("migration lost username/default nickname", nickname, err)
	}
	exec(`INSERT INTO users(id,username,password_hash) VALUES('other','OtherLogin123','unused')`)
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,password_hash) VALUES('invalid','new_user','unused')`); err == nil {
		t.Fatal("database accepted new underscore username")
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET username='changed_name' WHERE id='other'`); err == nil {
		t.Fatal("database accepted changed underscore username")
	}
	token := strings.Repeat("a", 64)
	exec(`INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,'owner','csrf',now()+interval '1 day'),($2,'other','csrf',now()+interval '1 day')`, hash(token), hash(strings.Repeat("b", 64)))
	song, version := ID(), ID()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO files(id,storage_key,original_filename,sha256,byte_size,media_type) VALUES('t','tja','chart.tja',repeat('a',64),1,'application/octet-stream'),('a','audio','song.mp3',repeat('b',64),1,'audio/mpeg')`,
		`INSERT INTO charts(id,owner_id,current_version_id) VALUES('` + song + `','owner','` + version + `')`,
		`INSERT INTO chart_versions(id,chart_id,version_number,title,bpm,duration,encoding,wave_filename,tja_file_id,audio_file_id,validation_version) VALUES('` + version + `','` + song + `',1,'Profile song',120,1,'utf-8','song.mp3','t','a','test')`,
		`INSERT INTO difficulties(version_id,block_index,course,level,player,style,maker) VALUES('` + version + `',0,'Oni',5,'','Single','')`,
		`INSERT INTO scores(id,user_id,song_id,version_id,block_index,difficulty,good,ok,bad,score,drumroll,max_combo,payload_digest) VALUES('score','owner','` + song + `','` + version + `',0,'Oni',1,0,0,100,0,1,repeat('a',64))`,
	} {
		if _, err := tx.Exec(ctx, query); err != nil {
			tx.Rollback(ctx)
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	handler := New(pool, Config{Origin: "http://localhost"}).Handler()
	call := func(method, path, body, cookie, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Origin", "http://localhost")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", csrf)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "ourtaiko_session", Value: cookie})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	expect := func(w *httptest.ResponseRecorder, code int) {
		t.Helper()
		if w.Code != code {
			t.Fatalf("want %d, got %d: %s", code, w.Code, w.Body.String())
		}
	}
	patch := func(body string) *httptest.ResponseRecorder { return call("PATCH", "/api/v1/me", body, token, "csrf") }
	expect(call("PATCH", "/api/v1/me", `{"nickname":"changed"}`, "", "csrf"), 401)
	expect(call("PATCH", "/api/v1/me", `{"nickname":"changed"}`, token, "wrong"), 403)
	for _, body := range []string{`{}`, `{"nickname":null}`, `{"nickname":""}`, `{"nickname":"   "}`, `{"nickname":"line\nname"}`, `{"nickname":"a\u0000b"}`, `{"nickname":"a\u2028b"}`, `{"nickname":"` + strings.Repeat("中", 41) + `"}`} {
		expect(patch(body), 422)
	}
	for _, body := range []string{`{"nickname":"name","username":"changed"}`, `{"nickname":"name","userId":"other"}`, `{"nickname":"name","password":"changed"}`, `{"nickname":"name","email":"changed@example.com"}`, `{"nickname":"name"} {"nickname":"other"}`, `{"nickname":123}`} {
		expect(patch(body), 400)
	}
	changed := patch(`{"nickname":"  雾雨 🎵  "}`)
	expect(changed, 200)
	var result struct {
		User User   `json:"user"`
		CSRF string `json:"csrfToken"`
	}
	if err := json.Unmarshal(changed.Body.Bytes(), &result); err != nil || result.User.ID != "owner" || result.User.Username != "private_owner" || result.User.Nickname != "雾雨 🎵" || result.CSRF != "csrf" {
		t.Fatal(changed.Body.String(), err)
	}
	current := call("GET", "/api/v1/me", "", token, "")
	expect(current, 200)
	if !strings.Contains(current.Body.String(), `"nickname":"雾雨 🎵"`) {
		t.Fatal(current.Body.String())
	}
	login := call("POST", "/api/v1/auth/login", `{"username":"private_owner","password":"test-password"}`, "", "")
	expect(login, 200)
	if !strings.Contains(login.Body.String(), `"nickname":"雾雨 🎵"`) {
		t.Fatal(login.Body.String())
	}
	// Public responses expose the display name, and searches cannot reveal the login name.
	for _, path := range []string{"/api/v1/charts", "/api/v1/charts/" + song, "/api/v1/charts/" + song + "/leaderboard?difficulty=Oni"} {
		w := call("GET", path, "", "", "")
		expect(w, 200)
		if strings.Contains(w.Body.String(), "private_owner") || strings.Contains(w.Body.String(), `"username"`) || !strings.Contains(w.Body.String(), "雾雨 🎵") {
			t.Fatal("public username exposure or missing nickname", path, w.Body.String())
		}
	}
	hidden := call("GET", "/api/v1/charts?q=private_owner", "", "", "")
	expect(hidden, 200)
	if !strings.Contains(hidden.Body.String(), `"total":0`) {
		t.Fatal("search still uses username", hidden.Body.String())
	}
	found := call("GET", "/api/v1/charts?q=%E9%9B%BE%E9%9B%A8", "", "", "")
	expect(found, 200)
	if !strings.Contains(found.Body.String(), `"total":1`) {
		t.Fatal("nickname search missed chart", found.Body.String())
	}
	// Duplicate display names are allowed without changing account identity or score ownership.
	duplicate := call("PATCH", "/api/v1/me", `{"nickname":"雾雨 🎵"}`, strings.Repeat("b", 64), "csrf")
	expect(duplicate, 200)
	var owner string
	if err := pool.QueryRow(ctx, `SELECT user_id FROM scores WHERE id='score'`).Scan(&owner); err != nil || owner != "owner" {
		t.Fatal("nickname changed score ownership", owner, err)
	}
	if err := pool.QueryRow(ctx, `SELECT owner_id FROM charts WHERE id=$1`, song).Scan(&owner); err != nil || owner != "owner" {
		t.Fatal("nickname changed upload ownership", owner, err)
	}
	// Forty non-BMP characters are forty characters, not eighty UTF-16 code units.
	expect(patch(`{"nickname":"`+strings.Repeat("🎵", 40)+`"}`), 200)
}

func TestRegistrationUsernameRule(t *testing.T) {
	handler := New(nil, Config{Origin: "http://localhost"}).Handler()
	for _, name := range []string{"ab", "new_user", "new-user", "用户", "Alice Bob", " Alice", "Alice\n", strings.Repeat("a", 25)} {
		t.Run(name, func(t *testing.T) {
			body, _ := json.Marshal(credentials{Username: name, Password: "test-password"})
			r := httptest.NewRequest("POST", "/api/v1/auth/register", strings.NewReader(string(body)))
			r.Header.Set("Origin", "http://localhost")
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 422 || !strings.Contains(w.Body.String(), "CREDENTIALS_INVALID") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	for _, name := range []string{"Alice123", "123", "abc", strings.Repeat("Z", 24)} {
		if !usernamePattern.MatchString(name) {
			t.Fatal("valid username rejected", name)
		}
	}
}
