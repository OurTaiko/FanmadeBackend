package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"ourtaiko.dev/fanmade/api/internal/database"
)

func scoreTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_TEST_URL")
	if url == "" {
		t.Skip("set DATABASE_TEST_URL to test score HTTP/SQL integration")
	}
	ctx := context.Background()
	admin, err := database.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("score_api_test_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+name); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec(ctx, "DROP SCHEMA "+name+" CASCADE"); admin.Close() })
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	storage := t.TempDir()
	for i := 0; i < 2; i++ {
		if err = database.Migrate(ctx, pool, storage); err != nil {
			t.Fatal(err)
		}
	}
	return pool
}

func TestSubmitScore(t *testing.T) {
	pool := scoreTestDB(t)
	ctx := context.Background()
	const song = "11111111111111111111111111111111"
	const otherSong = "22222222222222222222222222222222"
	const origin = "http://127.0.0.1:5173"
	const csrf = "test-csrf"
	const cookie = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const cookie2 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	_, err := pool.Exec(ctx, `INSERT INTO users(id,username,password_hash) VALUES('89b6ef3a5cb57b6e04f74711d15a8a5f','tester','unused'),('d1ee9badde8bb6f733b2315472711632','tester2','unused');`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,'89b6ef3a5cb57b6e04f74711d15a8a5f',$3,now()+interval '1 day'),($2,'d1ee9badde8bb6f733b2315472711632',$3,now()+interval '1 day')`, hash(cookie), hash(cookie2), csrf)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO files(id,storage_key,original_filename,sha256,byte_size,media_type) VALUES
 ('t','tja','test.tja',repeat('a',64),1,'application/octet-stream'),('a','ogg','test.ogg',repeat('b',64),1,'audio/ogg');
 INSERT INTO charts(id,owner_id) VALUES('11111111111111111111111111111111','89b6ef3a5cb57b6e04f74711d15a8a5f');
 INSERT INTO chart_data(chart_id,title,bpm,duration,encoding,wave_filename,tja_file_id,audio_file_id,validation_version) VALUES('11111111111111111111111111111111','Test',120,10,'utf-8','test.ogg','t','a','tja-upload-v2');
 INSERT INTO difficulties(chart_id,block_index,course,level,player,style) VALUES
 ('11111111111111111111111111111111',0,'Oni',5,'','Single'),
 ('11111111111111111111111111111111',1,'Oni',5,'P1','Double'),
 ('11111111111111111111111111111111',2,'Oni',5,'P2','Double'),
 ('11111111111111111111111111111111',3,'Hard',5,'','Double'),
 ('11111111111111111111111111111111',4,'Easy',5,'','Single'),
 ('11111111111111111111111111111111',5,'Easy',5,'','Single'),
 ('11111111111111111111111111111111',6,'Edit',5,'','Single');`)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	handler := testServer(t, pool, Config{Origin: origin}).Handler()
	nativeToken := strings.Repeat("c", 64)
	if _, err = pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,'89b6ef3a5cb57b6e04f74711d15a8a5f','',now()+interval '1 day')`, hash("game:"+nativeToken)); err != nil {
		t.Fatal(err)
	}
	native := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+nativeToken)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "game-integration-score-1")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	gameBody := `{"songId":"` + song + `","difficulty":"Oni","good":5,"ok":1,"bad":0,"score":6000,"drumroll":2,"max_combo":6}`
	if w := native("POST", "/api/v1/game/scores", gameBody); w.Code != 201 || !strings.Contains(w.Body.String(), `"max_combo":6`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := native("POST", "/api/v1/game/scores", gameBody); w.Code != 200 || !strings.Contains(w.Body.String(), `"max_combo":6`) {
		t.Fatal(w.Code, w.Body.String())
	}
	// Pre-migration receipts hashed the removed field; retries now use song ID alone.
	oldBody := strings.Replace(gameBody, `"difficulty"`, `"versionId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","difficulty"`, 1)
	if _, err := pool.Exec(ctx, `UPDATE scores SET payload_digest=$1 WHERE idempotency_key='game-integration-score-1'`, hash(oldBody)); err != nil {
		t.Fatal(err)
	}
	if w := native("POST", "/api/v1/game/scores", gameBody); w.Code != 200 || strings.Contains(w.Body.String(), "versionId") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := native("POST", "/api/v1/game/scores", oldBody); w.Code != 400 {
		t.Fatal("removed field accepted", w.Code, w.Body.String())
	}
	if w := native("GET", "/api/v1/game/bootstrap", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"songIdOnly":true`) || strings.Contains(w.Body.String(), "versionId") || !strings.Contains(w.Body.String(), `"good":5`) || !strings.Contains(w.Body.String(), `"max_combo":6`) {
		t.Fatal(w.Code, w.Body.String())
	}
	// Existing personal scores must never leak through anonymous bootstrap,
	// including when a browser session cookie accompanies the request.
	for _, browserCookie := range []string{"", cookie, cookie2} {
		r := httptest.NewRequest("GET", "/api/v1/game/bootstrap", nil)
		if browserCookie != "" {
			r.AddCookie(&http.Cookie{Name: "ourtaiko_session", Value: browserCookie})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"user":null`) || !strings.Contains(w.Body.String(), `"scores":[]`) {
			t.Fatal("guest received personal data", w.Code, w.Body.String())
		}
	}
	if w := native("POST", "/api/v1/game/scores", strings.Replace(gameBody, `"max_combo":6`, `"max_combo":5`, 1)); w.Code != 409 || !strings.Contains(w.Body.String(), "IDEMPOTENCY_CONFLICT") {
		t.Fatal(w.Code, w.Body.String())
	}
	// Leave the existing count assertions isolated from this native submission.
	if _, err = pool.Exec(ctx, `DELETE FROM scores WHERE idempotency_key='game-integration-score-1'`); err != nil {
		t.Fatal(err)
	}

	for _, raw := range []string{"", "null", "0", "1", "2", "3"} {
		body := gameBody
		status := 0
		if raw != "" {
			body = strings.TrimSuffix(body, "}") + `,"ClearStatus":` + raw + `}`
			if raw != "null" {
				if err := json.Unmarshal([]byte(raw), &status); err != nil {
					t.Fatal(err)
				}
			}
		}
		field := fmt.Sprintf(`"ClearStatus":%d`, status)
		w := native("POST", "/api/v1/game/scores", body)
		if w.Code != 201 || !strings.Contains(w.Body.String(), field) {
			t.Fatal("clear status receipt", w.Code, w.Body.String())
		}
		var first Score
		if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil {
			t.Fatal(err)
		}
		var stored int
		if err := pool.QueryRow(ctx, `SELECT clear_status FROM scores WHERE id=$1`, first.ID).Scan(&stored); err != nil || stored != status {
			t.Fatal("clear status not persisted", stored, err)
		}
		if retry := native("POST", "/api/v1/game/scores", body); retry.Code != 200 || !strings.Contains(retry.Body.String(), first.ID) || !strings.Contains(retry.Body.String(), field) {
			t.Fatal("clear status retry failed", retry.Code, retry.Body.String())
		}
		changed := strings.TrimSuffix(gameBody, "}") + fmt.Sprintf(`,"ClearStatus":%d}`, (status+1)%4)
		if conflict := native("POST", "/api/v1/game/scores", changed); conflict.Code != 409 {
			t.Fatal("clear status must participate in idempotency", conflict.Code, conflict.Body.String())
		}
		for _, path := range []string{"/api/v1/game/bootstrap", "/api/v1/charts/" + song + "/leaderboard"} {
			if w := native("GET", path, ""); w.Code != 200 || !strings.Contains(w.Body.String(), first.ID) || !strings.Contains(w.Body.String(), field) {
				t.Fatal("clear status missing from query", path, w.Code, w.Body.String())
			}
		}
		if _, err := pool.Exec(ctx, `DELETE FROM scores WHERE id=$1`, first.ID); err != nil {
			t.Fatal(err)
		}
	}

	body := `{"songId":"` + song + `","difficulty":"Oni","good":300,"ok":10,"bad":2,"score":900000,"drumroll":50,"max_combo":250,"ClearStatus":1}`
	var requestCount atomic.Uint32
	call := func(body, key, token string, headers map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/scores", strings.NewReader(body))
		// This validation suite exceeds the per-IP minute budget. Model separate
		// clients while keeping the real limiter enabled in the handler.
		r.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", requestCount.Add(1))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", csrf)
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "ourtaiko_session", Value: token})
		}
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	assertStatus := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("want %d got %d: %s", status, w.Code, w.Body.String())
		}
		if code != "" {
			var result struct{ Code string }
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Code != code {
				t.Fatalf("want %s: %s", code, w.Body.String())
			}
		}
	}
	for _, tc := range []struct {
		name, body, key, token string
		headers                map[string]string
		status                 int
		code                   string
	}{
		{"anonymous", body, "", "", nil, 401, "UNAUTHORIZED"},
		{"csrf", body, "", cookie, map[string]string{"X-CSRF-Token": ""}, 403, "CSRF_INVALID"},
		{"origin", body, "", cookie, map[string]string{"Origin": "https://invalid.example"}, 403, "ORIGIN_INVALID"},
		{"media", body, "", cookie, map[string]string{"Content-Type": "text/plain"}, 415, "CONTENT_TYPE_INVALID"},
		{"negative", strings.Replace(body, `"good":300`, `"good":-1`, 1), "", cookie, nil, 422, "SCORE_INVALID"},
		{"clear negative", strings.Replace(body, `"ClearStatus":1`, `"ClearStatus":-1`, 1), "", cookie, nil, 422, "SCORE_INVALID"},
		{"clear range", strings.Replace(body, `"ClearStatus":1`, `"ClearStatus":4`, 1), "", cookie, nil, 422, "SCORE_INVALID"},
		{"clear fraction", strings.Replace(body, `"ClearStatus":1`, `"ClearStatus":1.5`, 1), "", cookie, nil, 400, "REQUEST_INVALID"},
		{"clear string", strings.Replace(body, `"ClearStatus":1`, `"ClearStatus":"1"`, 1), "", cookie, nil, 400, "REQUEST_INVALID"},
		{"clear boolean", strings.Replace(body, `"ClearStatus":1`, `"ClearStatus":true`, 1), "", cookie, nil, 400, "REQUEST_INVALID"},
		{"missing", strings.Replace(body, `"good":300,`, "", 1), "", cookie, nil, 422, "SCORE_INVALID"},
		{"null", strings.Replace(body, `"ok":10`, `"ok":null`, 1), "", cookie, nil, 422, "SCORE_INVALID"},
		{"fraction", strings.Replace(body, `"bad":2`, `"bad":2.5`, 1), "", cookie, nil, 400, "REQUEST_INVALID"},
		{"count range", strings.Replace(body, `"drumroll":50`, `"drumroll":2147483648`, 1), "", cookie, nil, 422, "SCORE_INVALID"},
		{"combo missing", strings.Replace(body, `,"max_combo":250`, "", 1), "", cookie, nil, 422, "SCORE_INVALID"},
		{"combo null", strings.Replace(body, `"max_combo":250`, `"max_combo":null`, 1), "", cookie, nil, 422, "SCORE_INVALID"},
		{"combo negative", strings.Replace(body, `"max_combo":250`, `"max_combo":-1`, 1), "", cookie, nil, 422, "SCORE_INVALID"},
		{"combo range", strings.Replace(body, `"max_combo":250`, `"max_combo":2147483648`, 1), "", cookie, nil, 422, "SCORE_INVALID"},
		{"combo fraction", strings.Replace(body, `"max_combo":250`, `"max_combo":1.5`, 1), "", cookie, nil, 400, "REQUEST_INVALID"},
		{"combo string", strings.Replace(body, `"max_combo":250`, `"max_combo":"250"`, 1), "", cookie, nil, 400, "REQUEST_INVALID"},
		{"score range", strings.Replace(body, `"score":900000`, `"score":9007199254740992`, 1), "", cookie, nil, 422, "SCORE_INVALID"},
		{"overflow", strings.Replace(body, `"good":300`, `"good":999999999999999999999`, 1), "", cookie, nil, 400, "REQUEST_INVALID"},
		{"spoof user", strings.TrimSuffix(body, "}") + `,"userId":"d1ee9badde8bb6f733b2315472711632"}`, "", cookie, nil, 400, "REQUEST_INVALID"},
		{"trailing", body + " {}", "", cookie, nil, 400, "REQUEST_INVALID"},
		{"null body", "null", "", cookie, nil, 422, "SCORE_INVALID"},
		{"unknown course", strings.Replace(body, "Oni", "Invalid", 1), "", cookie, nil, 422, "SCORE_INVALID"},
		{"missing song", strings.Replace(body, song, otherSong, 1), "", cookie, nil, 404, "CHART_NOT_FOUND"},
		{"missing difficulty", strings.Replace(body, "Oni", "Normal", 1), "", cookie, nil, 404, "DIFFICULTY_NOT_FOUND"},
		{"double", strings.Replace(body, "Oni", "Hard", 1), "", cookie, nil, 422, "DOUBLE_SCORE_UNSUPPORTED"},
		{"ambiguous", strings.Replace(body, "Oni", "Easy", 1), "", cookie, nil, 409, "DIFFICULTY_AMBIGUOUS"},
		{"invalid key", body, "short", cookie, nil, 400, "IDEMPOTENCY_KEY_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) { assertStatus(call(tc.body, tc.key, tc.token, tc.headers), tc.status, tc.code) })
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM scores`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejections inserted scores: %d %v", count, err)
	}
	const key = "score-request-0001"
	w := call(body, key, cookie, nil)
	assertStatus(w, 201, "")
	var first Score
	if err = json.Unmarshal(w.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if first.ClearStatus != 1 || !strings.Contains(w.Body.String(), `"ClearStatus":1`) {
		t.Fatal("browser upload lost clear status", w.Body.String())
	}
	if first.UserID != "89b6ef3a5cb57b6e04f74711d15a8a5f" || first.BlockIndex != 0 || first.Good != 300 || first.OK != 10 || first.Bad != 2 || first.Score != 900000 || first.Drumroll != 50 || first.MaxCombo != 250 || first.SubmittedAt.IsZero() {
		t.Fatalf("bad receipt: %+v", first)
	}
	stored, err := readScore(pool.QueryRow(ctx, `SELECT `+scoreColumns+` FROM scores WHERE id=$1`, first.ID))
	// Drivers may use time.Local for a UTC database timestamp; compare the same zone.
	stored.SubmittedAt = stored.SubmittedAt.UTC()
	first.SubmittedAt = first.SubmittedAt.UTC()
	if err != nil || stored != first {
		t.Fatalf("stored receipt mismatch: %+v %v", stored, err)
	}
	w = call(strings.Replace(body, "Oni", "oni", 1), key, cookie, nil)
	assertStatus(w, 200, "")
	var replay Score
	json.Unmarshal(w.Body.Bytes(), &replay)
	if replay.ID != first.ID || replay.MaxCombo != 250 {
		t.Fatal("retry inserted a new score")
	}
	assertStatus(call(strings.Replace(body, "900000", "900001", 1), key, cookie, nil), 409, "IDEMPOTENCY_CONFLICT")
	assertStatus(call(strings.Replace(body, `"max_combo":250`, `"max_combo":249`, 1), key, cookie, nil), 409, "IDEMPOTENCY_CONFLICT")
	assertStatus(call(body, key, cookie2, nil), 201, "")
	assertStatus(call(body, "", cookie, nil), 201, "")
	assertStatus(call(body, "", cookie, nil), 201, "")
	zero := strings.NewReplacer(`"max_combo":250`, `"max_combo":0`, "Oni", "ura", "300", "0", "10", "0", "2,", "0,", "900000", "0", "50", "0").Replace(body)
	w = call(zero, "", cookie, nil)
	assertStatus(w, 201, "")
	var edit Score
	json.Unmarshal(w.Body.Bytes(), &edit)
	if edit.Difficulty != "Edit" || edit.Score != 0 || edit.Good != 0 || edit.MaxCombo != 0 {
		t.Fatalf("zero/alias: %+v", edit)
	}
	// Concurrent retries must produce one persistent record and one receipt ID.
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); responses <- call(body, "score-concurrent-0001", cookie, nil) }()
	}
	wg.Wait()
	close(responses)
	created := 0
	ids := map[string]bool{}
	for r := range responses {
		if r.Code == 201 {
			created++
		} else {
			assertStatus(r, 200, "")
		}
		var v Score
		json.Unmarshal(r.Body.Bytes(), &v)
		ids[v.ID] = true
	}
	if created != 1 || len(ids) != 1 {
		t.Fatalf("concurrency: %d created %d IDs", created, len(ids))
	}
	// A receipt survives a rename and retries still return it after unpublishing.
	if _, err = pool.Exec(ctx, `UPDATE chart_data SET title='Renamed' WHERE chart_id=$1`, song); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE charts SET status='deleted' WHERE id=$1`, song); err != nil {
		t.Fatal(err)
	}
	assertStatus(call(body, "", cookie, nil), 404, "CHART_NOT_FOUND")
	assertStatus(call(body, key, cookie, nil), 200, "")
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM scores`).Scan(&count); err != nil || count != 6 {
		t.Fatalf("unexpected persisted plays: %d %v", count, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE scores SET max_combo=-1 WHERE id=$1`, first.ID); err == nil {
		t.Fatal("database accepted negative maximum combo")
	}
	// Even SQL writes cannot attach a score to a Double or different difficulty.
	if _, err = pool.Exec(ctx, `UPDATE scores SET block_index=1 WHERE id=$1`, first.ID); err == nil {
		t.Fatal("database accepted Double target")
	}
	if _, err = pool.Exec(ctx, `UPDATE scores SET difficulty='Hard' WHERE id=$1`, first.ID); err == nil {
		t.Fatal("database accepted mismatched difficulty")
	}
}
