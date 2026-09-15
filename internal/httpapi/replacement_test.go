package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestChartReplacement(t *testing.T) {
	pool := scoreTestDB(t)
	ctx := context.Background()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO users(id,username,password_hash,is_admin) VALUES('owner','owner','unused',false),('other','other','unused',false),('admin','admin','unused',true)`)
	tokens := map[string]string{"owner": strings.Repeat("a", 64), "other": strings.Repeat("b", 64), "admin": strings.Repeat("c", 64)}
	for user, token := range tokens {
		mustExec(`INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,$3,'csrf',now()+interval '1 day'),($2,$3,'csrf',now()+interval '1 day')`, hash(token), hash("game:"+token), user)
	}
	storage := t.TempDir()
	app := New(pool, Config{Origin: "http://localhost", Storage: storage})
	handler := app.Handler()
	audio, err := os.ReadFile("../audio/testdata/cbr.mp3")
	if err != nil {
		t.Fatal(err)
	}
	source := "TITLE:Original\nMAKER:A\nBPM:120\nWAVE:cbr.mp3\nCOURSE:Hard\nLEVEL:5\n#START\n1000,\n#END\n"
	replacement := strings.Replace(source, "TITLE:Original", "TITLE:Updated", 1) + "COURSE:Oni\nLEVEL:7\n#START\n1100,\n#END\n"
	request := func(user, method, path, key, chartSource, audioName string, audioBytes []byte, fields map[string]string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		tf, _ := form.CreateFormFile("tja", "chart.tja")
		tf.Write([]byte(chartSource))
		if audioBytes != nil {
			af, _ := form.CreateFormFile("audio", audioName)
			af.Write(audioBytes)
		}
		for name, value := range fields {
			form.WriteField(name, value)
		}
		form.Close()
		r := httptest.NewRequest(method, path, &body)
		r.Header.Set("Content-Type", form.FormDataContentType())
		r.Header.Set("Origin", "http://localhost")
		r.Header.Set("X-CSRF-Token", "csrf")
		r.Header.Set("Idempotency-Key", key)
		r.AddCookie(&http.Cookie{Name: "ourtaiko_session", Value: tokens[user]})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	status := func(w *httptest.ResponseRecorder, code int) {
		t.Helper()
		if w.Code != code {
			t.Fatalf("want %d got %d: %s", code, w.Code, w.Body.String())
		}
	}
	decode := func(w *httptest.ResponseRecorder, code int) Chart {
		t.Helper()
		status(w, code)
		var c Chart
		if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+tokens["owner"])
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	score := func(c Chart, user string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"songId":%q,"versionId":%q,"difficulty":"Hard","good":1,"ok":0,"bad":0,"score":100,"drumroll":0,"max_combo":1}`, c.ID, c.VersionID)
		r := httptest.NewRequest("POST", "/api/v1/game/scores", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+tokens[user])
		r.Header.Set("Idempotency-Key", ID())
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	original := decode(request("owner", "POST", "/api/v1/charts", ID(), source, "cbr.mp3", audio, nil), 201)
	unrelated := decode(request("other", "POST", "/api/v1/charts", ID(), source, "cbr.mp3", audio, nil), 201)
	status(score(original, "owner"), 201)
	status(score(original, "other"), 201)
	status(score(unrelated, "other"), 201)
	original, err = app.chart(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/charts/" + original.ID + "/files"
	fields := map[string]string{"expectedVersionId": original.VersionID, "confirmReset": "true", "description": "new description", "difficultyMakers": `[{"blockIndex":0,"maker":"A"},{"blockIndex":1,"maker":"B"}]`}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	unchanged := func() {
		t.Helper()
		c, err := app.chart(ctx, original.ID)
		if err != nil || c.VersionID != original.VersionID || count(`SELECT count(*) FROM scores WHERE song_id=$1`, original.ID) != 2 {
			t.Fatal("failed update changed original", c.VersionID, err)
		}
		if b, err := os.ReadFile(filepath.Join(storage, original.TJAKey)); err != nil || string(b) != source {
			t.Fatal("old TJA lost", err)
		}
		if b, err := os.ReadFile(filepath.Join(storage, original.AudioKey)); err != nil || !bytes.Equal(b, audio) {
			t.Fatal("old audio lost", err)
		}
	}
	for _, test := range []struct {
		name, user, source, audioName string
		audio                         []byte
		fields                        map[string]string
		code                          int
	}{
		{"permission", "other", replacement, "", nil, fields, 403},
		{"login", "anonymous", replacement, "", nil, fields, 401},
		{"confirmation", "owner", replacement, "", nil, map[string]string{"expectedVersionId": original.VersionID}, 400},
		{"stale", "owner", replacement, "", nil, map[string]string{"expectedVersionId": ID(), "confirmReset": "true"}, 409},
		{"invalid TJA", "owner", "broken", "", nil, fields, 422},
		{"invalid audio", "owner", replacement, "cbr.mp3", []byte("broken"), fields, 422},
		{"wrong makers", "owner", source, "", nil, fields, 422},
	} {
		t.Run(test.name, func(t *testing.T) {
			status(request(test.user, "PUT", path, ID(), test.source, test.audioName, test.audio, test.fields), test.code)
			unchanged()
		})
	}
	// Fail after the transaction has deleted old rows, proving all deletions roll back.
	mustExec(`CREATE FUNCTION fail_replacement() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.maker='B' THEN RAISE EXCEPTION 'test write failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_replacement BEFORE INSERT ON difficulties FOR EACH ROW EXECUTE FUNCTION fail_replacement()`)
	status(request("owner", "PUT", path, ID(), replacement, "", nil, fields), 503)
	unchanged()
	mustExec(`DROP TRIGGER fail_replacement ON difficulties; DROP FUNCTION fail_replacement()`)
	// Historical versions must be deleted as well, even if old imports shared audio.
	history := ID()
	mustExec(`INSERT INTO chart_versions(id,chart_id,version_number,title,subtitle,bpm,offset_seconds,demo_start,duration,encoding,wave_filename,tja_file_id,audio_file_id,validation_version) SELECT $1,chart_id,2,title,subtitle,bpm,offset_seconds,demo_start,duration,encoding,wave_filename,tja_file_id,audio_file_id,validation_version FROM chart_versions WHERE id=$2`, history, original.VersionID)
	mustExec(`INSERT INTO difficulties(version_id,block_index,course,level,player,style,maker) VALUES($1,0,'Hard',5,'','Single','A')`, history)
	mustExec(`INSERT INTO scores(id,user_id,song_id,version_id,block_index,difficulty,good,ok,bad,score,drumroll,max_combo,payload_digest) VALUES($1,'owner',$2,$3,0,'Hard',1,0,0,100,0,1,repeat('a',64))`, ID(), original.ID, history)
	key := ID()
	updated := decode(request("owner", "PUT", path, key, replacement, "", nil, fields), 200)
	if updated.ID != original.ID || updated.VersionID == original.VersionID || updated.Title != "Updated" || updated.Maker != "A | B" || updated.Description != "new description" || len(updated.Difficulties) != 2 || updated.AudioHash != original.AudioHash {
		t.Fatalf("bad updated chart: %+v", updated)
	}
	if count(`SELECT count(*) FROM scores WHERE song_id=$1`, original.ID) != 0 || count(`SELECT count(*) FROM chart_versions WHERE chart_id=$1`, original.ID) != 1 || count(`SELECT count(*) FROM difficulties WHERE version_id=ANY($1)`, []string{original.VersionID, history}) != 0 {
		t.Fatal("old rows remain")
	}
	if count(`SELECT count(*) FROM scores WHERE song_id=$1`, unrelated.ID) != 1 || count(`SELECT count(*) FROM files`) != 4 {
		t.Fatal("unrelated data changed or old file rows remain")
	}
	for _, key := range []string{original.TJAKey, original.AudioKey} {
		if _, err := os.Stat(filepath.Join(storage, key)); !os.IsNotExist(err) {
			t.Fatal("old file remains", key, err)
		}
	}
	for _, kind := range []string{"tja", "audio", "download"} {
		status(get("/api/v1/charts/"+original.ID+"/versions/"+original.VersionID+"/"+kind), 404)
	}
	status(get("/api/v1/charts/"+updated.ID+"/versions/"+updated.VersionID+"/audio"), 200)
	bootstrap := get("/api/v1/game/bootstrap")
	status(bootstrap, 200)
	if !strings.Contains(bootstrap.Body.String(), `"scores":[]`) {
		t.Fatal("game still receives old scores", bootstrap.Body.String())
	}
	leaderboard := get("/api/v1/charts/" + updated.ID + "/leaderboard?difficulty=Hard")
	status(leaderboard, 200)
	if !strings.Contains(leaderboard.Body.String(), `"items":[]`) {
		t.Fatal("leaderboard retained old scores")
	}
	status(score(original, "owner"), 409)
	status(score(updated, "owner"), 201)
	retry := decode(request("owner", "PUT", path, key, replacement, "", nil, fields), 200)
	if retry.VersionID != updated.VersionID || count(`SELECT count(*) FROM scores WHERE song_id=$1`, original.ID) != 1 {
		t.Fatal("retry deleted the new score")
	}
	status(request("owner", "PUT", path, key, source, "", nil, fields), 409)
	status(request("owner", "PUT", path, ID(), replacement, "", nil, fields), 409)
	// Two valid updates from the same page: only one may replace the chart.
	nextFields := map[string]string{"expectedVersionId": updated.VersionID, "confirmReset": "true"}
	nextSource := strings.Replace(replacement, "cbr.mp3", "next.mp3", 1)
	var wg sync.WaitGroup
	replies := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			replies <- request("admin", "PUT", path, ID(), nextSource, "next.mp3", audio, nextFields)
		}()
	}
	wg.Wait()
	close(replies)
	successes, conflicts := 0, 0
	for w := range replies {
		if w.Code == 200 {
			successes++
		} else if w.Code == 409 {
			conflicts++
		} else {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal("concurrent replacement", successes, conflicts)
	}
	final, err := app.chart(ctx, original.ID)
	if err != nil || final.AudioName != "next.mp3" || final.OwnerID != "owner" || count(`SELECT count(*) FROM scores WHERE song_id=$1`, original.ID) != 0 {
		t.Fatal("admin replacement", final, err)
	}
	status(request("owner", "PUT", path, key, replacement, "", nil, fields), 409)
	if count(`SELECT count(*) FROM retired_files`) != 0 || count(`SELECT count(*) FROM files`) != 4 {
		t.Fatal("cleanup incomplete")
	}
}

func TestRetiredFileCleanupRetries(t *testing.T) {
	pool := scoreTestDB(t)
	ctx := context.Background()
	storage := t.TempDir()
	app := New(pool, Config{Storage: storage})
	key := "objects/old/audio"
	// A nonempty directory cannot be removed as a file; keep its durable queue row.
	if err := os.MkdirAll(filepath.Join(storage, key), 0700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(storage, key, "blocked")
	if err := os.WriteFile(child, []byte("retry"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO retired_files(storage_key) VALUES($1)`, key); err != nil {
		t.Fatal(err)
	}
	if err := app.deleteRetiredFiles(ctx); err == nil {
		t.Fatal("expected cleanup failure")
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM retired_files`).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if err := os.Remove(child); err != nil {
		t.Fatal(err)
	}
	if err := app.deleteRetiredFiles(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM retired_files`).Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if _, err := os.Stat(filepath.Join(storage, key)); !os.IsNotExist(err) {
		t.Fatal("retired object remains", err)
	}
}
