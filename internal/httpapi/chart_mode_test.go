package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestDoubleChartUploadScoresAndSearch(t *testing.T) {
	pool := scoreTestDB(t)
	ctx := context.Background()
	token := strings.Repeat("c", 64)
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,password_hash) VALUES('eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','doubletest','unused');`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','csrf',now()+interval '1 day'),($2,'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','',now()+interval '1 day')`, hash(token), hash("game:"+token)); err != nil {
		t.Fatal(err)
	}
	handler := testServer(t, pool, Config{Origin: "http://localhost", Storage: t.TempDir()}).Handler()
	audio, err := os.ReadFile("../audio/testdata/cbr.mp3")
	if err != nil {
		t.Fatal(err)
	}
	header := "TITLE:Double\nBPM:120\nWAVE:cbr.mp3\nCOURSE:Oni\nLEVEL:8\nSTYLE:Double\n"
	p1 := "#START P1\n1000,\n#END\n"
	p2 := "#START P2\n2000,\n#END\n"
	upload := func(source string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		tf, _ := form.CreateFormFile("tja", "double.tja")
		tf.Write([]byte(source))
		af, _ := form.CreateFormFile("audio", "cbr.mp3")
		af.Write(audio)
		form.WriteField("difficultyMakers", `[{"course":"Oni_1p","maker":"A"},{"course":"Oni_2p","maker":"B"}]`)
		form.Close()
		r := httptest.NewRequest("POST", "/api/v1/charts", &body)
		r.Header.Set("Content-Type", form.FormDataContentType())
		r.Header.Set("Origin", "http://localhost")
		r.Header.Set("X-CSRF-Token", "csrf")
		r.Header.Set("Idempotency-Key", ID())
		r.AddCookie(&http.Cookie{Name: "ourtaiko_session", Value: token})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, body := range []string{header + p1 + p1, header + "#START\n1000,\n#END", header + p1 + "COURSE:Hard\nLEVEL:5\n#START\n1000,\n#END"} {
		if w := upload(body); w.Code != 422 {
			t.Fatal("invalid chart accepted", w.Code, w.Body.String())
		}
	}
	w := upload(header + p2 + p1)
	var c Chart
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &c) != nil || c.IsSingle || len(c.Difficulties) != 2 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, key := range []string{"blockIndex", "cloudScoreEligible", "\"style\"", "\"player\""} {
		if strings.Contains(w.Body.String(), key) {
			t.Fatal("obsolete field exposed", key)
		}
	}
	if c.Difficulties[0].Course != "Oni_2p" || c.Difficulties[0].Maker != "B" {
		t.Fatal(c.Difficulties)
	}
	call := func(method, path, body, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	score := func(course string, points int) string {
		data, _ := json.Marshal(map[string]any{"songId": c.ID, "difficulty": course, "good": 10, "ok": 0, "bad": 0, "score": points, "drumroll": 0, "max_combo": 10, "ClearStatus": 2})
		return string(data)
	}
	for i, course := range []string{"oni_1p", "Oni_2p"} {
		key := ID()
		body := score(course, 1000+i)
		if w := call("POST", "/api/v1/game/scores", body, key); w.Code != 201 {
			t.Fatal(w.Code, w.Body.String())
		}
		if w := call("POST", "/api/v1/game/scores", body, key); w.Code != 200 {
			t.Fatal("retry", w.Code, w.Body.String())
		}
	}
	for _, course := range []string{"Oni", "Hard_1p"} {
		if w := call("POST", "/api/v1/game/scores", score(course, 2000), ID()); w.Code != 404 {
			t.Fatal("nonexistent course accepted", w.Code, w.Body.String())
		}
	}
	if w := call("POST", "/api/v1/game/scores", score("Oni_3p", 2000), ID()); w.Code != 422 {
		t.Fatal("invalid side accepted", w.Code, w.Body.String())
	}
	for i, course := range []string{"Oni_1p", "Oni_2p"} {
		w := call("GET", "/api/v1/charts/"+c.ID+"/leaderboard?difficulty="+course, "", "")
		var v leaderboardResponse
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Total != 1 || !v.Supported || len(v.Items) != 1 || v.Items[0].Difficulty != course || v.Items[0].Score.Score != int64(1000+i) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w = call("GET", "/api/v1/game/bootstrap", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"courseKeyedDifficulties":true`) || !strings.Contains(w.Body.String(), `"difficulty":"Oni_2p"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("GET", "/api/v1/game/search?course=Oni_1p", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), c.ID) {
		t.Fatal(w.Code, w.Body.String())
	}
	// Read locks serialize score validation with replacement of JSON metadata.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM charts WHERE id=$1 FOR UPDATE`, c.ID); err != nil {
		t.Fatal(err)
	}
	other, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Rollback(ctx)
	if _, err = other.Exec(ctx, `SET LOCAL lock_timeout='100ms'`); err != nil {
		t.Fatal(err)
	}
	if _, err = other.Exec(ctx, `UPDATE scores SET difficulty=difficulty WHERE song_id=$1`, c.ID); err == nil {
		t.Fatal("score validation did not lock its chart")
	}
}
