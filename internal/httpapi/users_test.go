package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"ourtaiko.dev/fanmade/api/internal/database"
)

func TestUserActivityMigrationAndAuthentication(t *testing.T) {
	pool := scoreTestDB(t)
	ctx := context.Background()
	id := ID()
	password, err := bcrypt.GenerateFromPassword([]byte("test-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO users(id,username,password_hash) VALUES($1,'ActivityUser',$2)`, id, string(password)); err != nil {
		t.Fatal(err)
	}
	app := testServer(t, pool, Config{Origin: "http://localhost"})
	// Replaying current migrations preserves unknown historical activity.
	if err = database.Migrate(ctx, pool, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	read := func() (*time.Time, *time.Time) {
		t.Helper()
		var first, last *time.Time
		if err := pool.QueryRow(ctx, `SELECT first_login_at,last_active_at FROM users WHERE id=$1`, id).Scan(&first, &last); err != nil {
			t.Fatal(err)
		}
		return first, last
	}
	if first, last := read(); first != nil || last != nil {
		t.Fatal("fabricated historical login")
	}
	call := func(method, path, body, cookie, bearer string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "ourtaiko_session", Value: cookie})
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, r)
		return w
	}
	w := call("POST", "/api/v1/game/login", `{"username":"ActivityUser","password":"wrong"}`, "", "")
	if w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, last := read(); last != nil {
		t.Fatal("failed login marked active")
	}
	cookie := seedWebSession(t, app, id)
	if w = call("GET", "/api/v1/me", "", cookie, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	first, last := read()
	if first != nil || last == nil {
		t.Fatal("authenticated legacy session invented login or missed activity")
	}
	w = call("POST", "/api/v1/game/login", `{"username":"ActivityUser","password":"test-password"}`, "", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var login struct {
		Token string `json:"accessToken"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}
	first, last = read()
	if first == nil || last == nil || first.After(*last) {
		t.Fatal("successful login not recorded")
	}
	initial := *first
	recent := *last
	for i := 0; i < 3; i++ {
		if w = call("GET", "/api/v1/game/bootstrap", "", "", login.Token); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	first, last = read()
	if !first.Equal(initial) || !last.Equal(recent) {
		t.Fatal("first login changed or activity writes not throttled")
	}
	// Move both recorded times back; a real website request must advance last only.
	if _, err = pool.Exec(ctx, `UPDATE users SET first_login_at=now()-interval '2 days',last_active_at=now()-interval '2 minutes' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	first, last = read()
	initial = *first
	old := *last
	if w = call("GET", "/api/v1/me", "", cookie, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	first, last = read()
	if !first.Equal(initial) || !last.After(old) {
		t.Fatal("web activity did not advance independently")
	}
	// Failed introspection must not refresh an expired activity timestamp.
	if _, err = pool.Exec(ctx, `UPDATE users SET last_active_at=now()-interval '2 minutes' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	_, last = read()
	old = *last
	app.Config.SSO.http = &http.Client{Transport: failingTransport{}}
	if w = call("GET", "/api/v1/me", "", cookie, ""); w.Code != 503 {
		t.Fatal(w.Code)
	}
	if _, last = read(); !last.Equal(old) {
		t.Fatal("failed SSO auth marked active")
	}
	// Concurrent first observations create one row, retain one first-login value.
	newID := ID()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := recordUserActivity(ctx, pool, newID, true); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var valid bool
	if err = pool.QueryRow(ctx, `SELECT first_login_at IS NOT NULL AND last_active_at>=first_login_at FROM users WHERE id=$1`, newID).Scan(&valid); err != nil || !valid {
		t.Fatal(err)
	}
}

func TestUserDirectoryPublicScopeAndStatistics(t *testing.T) {
	pool := scoreTestDB(t)
	ctx := context.Background()
	ids := []string{}
	for i := 0; i < 15; i++ {
		id := fmt.Sprintf("%032x", i+100)
		ids = append(ids, id)
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,nickname,password_hash) VALUES($1,$2,$3,'private-hash')`, id, fmt.Sprintf("PrivateLogin%d", i), fmt.Sprintf("鼓手 %02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	app := testServer(t, pool, Config{})
	// An SSO-only identity must never appear in the Fanmade directory.
	if _, err := pool.Exec(ctx, `INSERT INTO test_sso_users(id,username,nickname,password_hash) VALUES($1,'SsoOnly','SSO独有','private-hash')`, ID()); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids[:14] {
		if _, err := pool.Exec(ctx, `UPDATE users SET first_login_at=now()-interval '30 days'+$2*interval '1 hour',last_active_at=now()-$2*interval '1 hour' WHERE id=$1`, id, i); err != nil {
			t.Fatal(err)
		}
	}
	// Two published charts, one hidden and one deleted: only current public
	// charts/scores count. Joins must not multiply either statistic.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for i, status := range []string{"published", "published", "hidden", "deleted"} {
		song := fmt.Sprintf("%032x", i+200)
		if _, err = tx.Exec(ctx, `INSERT INTO charts(id,owner_id,status,title,bpm,duration,wave_filename) VALUES($1,$2,$3,'Public chart',120,10,'fixture')`, song, ids[0], status); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO chart_resources(chart_id,kind,storage_key,original_filename,sha256,byte_size,media_type) SELECT $1,kind,$1||kind,'fixture',repeat('a',64),1,'test' FROM unnest(ARRAY['tja','audio']) kind`, song); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `UPDATE charts c SET difficulties=c.difficulties||d.items FROM (SELECT chart_id,jsonb_agg(jsonb_build_object('course',course,'level',level,'maker',maker)) items FROM (VALUES ($1,'Oni',1,'')) v(chart_id,course,level,maker) GROUP BY chart_id) d WHERE c.id=d.chart_id`, song); err != nil {
			t.Fatal(err)
		}
		for j := 0; j < 2; j++ {
			if _, err = tx.Exec(ctx, `INSERT INTO scores(id,user_id,song_id,difficulty,good,ok,bad,score,drumroll,max_combo,payload_digest) VALUES($1,$2,$3,'Oni',1,0,0,1000,0,1,repeat('a',64))`, ID(), ids[0], song); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	get := func(query string) (*httptest.ResponseRecorder, UserDirectory) {
		t.Helper()
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/users"+query, nil))
		var result UserDirectory
		if w.Code == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
		}
		return w, result
	}
	w, result := get("")
	if w.Code != 200 || result.Total != 15 || len(result.Items) != 12 || result.Items[0].ID != ids[0] {
		t.Fatal(w.Code, w.Body.String())
	}
	if result.Items[0].ChartCount != 2 || result.Items[0].ScoreCount != 4 {
		t.Fatal("incorrect public statistics", w.Body.String())
	}
	var raw struct {
		Items []map[string]any `json:"items"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	public := map[string]bool{"id": true, "nickname": true, "avatarUrl": true, "firstLoginAt": true, "lastActiveAt": true, "chartCount": true, "scoreCount": true, "commentCount": true, "karma": true}
	for _, u := range raw.Items {
		for key := range u {
			if !public[key] {
				t.Fatal("unexpected public field", key, u)
			}
		}
		if len(u) != len(public) {
			t.Fatal("missing public fields", u)
		}
	}
	for _, secret := range []string{"PrivateLogin", "private-hash", "email", "isAdmin", "csrf", "token", "SSO独有"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("private/unrelated data leaked", secret)
		}
	}
	_, second := get("?page=2")
	if len(second.Items) != 3 || second.Items[2].FirstLoginAt != nil || second.Items[2].LastActiveAt != nil {
		t.Fatal("pagination/unknown times", second)
	}
	_, result = get("?sort=newest")
	if result.Items[0].ID != ids[13] {
		t.Fatal("newest order")
	}
	_, result = get("?q=" + url.QueryEscape("鼓手 00"))
	if result.Total != 1 || result.Items[0].ID != ids[0] {
		t.Fatal("nickname search")
	}
	_, result = get("?q=PrivateLogin")
	if result.Total != 0 || len(result.Items) != 0 {
		t.Fatal("private login searchable")
	}
	for _, query := range []string{"?page=0", "?page=bad", "?page=1.5", "?page=10001", "?sort=invalid", "?q=" + strings.Repeat("x", 201)} {
		if w, _ = get(query); w.Code != 400 {
			t.Fatal("invalid query accepted", query)
		}
	}
	// Exact owner filtering for the public chart link.
	w = httptest.NewRecorder()
	app.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/charts?owner="+ids[1], nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"total":0`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	app.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/charts?owner="+ids[0], nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"total":2`) {
		t.Fatal(w.Code, w.Body.String())
	}
	// A public space exposes the same allowlisted fields, even for a legacy user.
	spaceRequest := func(id string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/users/"+id, nil))
		return response
	}
	w = spaceRequest(ids[0])
	var space UserSpace
	if err = json.Unmarshal(w.Body.Bytes(), &space); err != nil || w.Code != 200 || space.User.ChartCount != 2 || space.User.ScoreCount != 4 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	if strings.Contains(w.Body.String(), "PrivateLogin") {
		t.Fatal("space leaked private identity")
	}
	for _, id := range []string{"bad-id", strings.Repeat("f", 32)} {
		if response := spaceRequest(id); response.Code != 404 {
			t.Fatal(response.Code)
		}
	}
	app.Config.SSO.http = &http.Client{Transport: failingTransport{}}
	w = spaceRequest(ids[0])
	if err = json.Unmarshal(w.Body.Bytes(), &space); err != nil || w.Code != 200 || space.ProfilesAvailable || space.User.Nickname != nil {
		t.Fatal("space outage fallback", w.Body.String(), err)
	}
	w, result = get("")
	if w.Code != 200 || result.ProfilesAvailable || result.Items[0].Nickname != nil || result.Total != 15 {
		t.Fatal("outage fallback", w.Body.String())
	}
	if w, _ = get("?q=hello"); w.Code != 503 {
		t.Fatal("outage masqueraded as no matches")
	}
}
