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

func TestDifficultyMakersFlow(t *testing.T) {
	pool := scoreTestDB(t)
	ctx := context.Background()
	token := strings.Repeat("d", 64)
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,password_hash) VALUES('037c0ccac142bf8602cbbe373b9a6f16','makertest','unused')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,'037c0ccac142bf8602cbbe373b9a6f16','csrf',now()+interval '1 day'),($2,'037c0ccac142bf8602cbbe373b9a6f16','csrf',now()+interval '1 day')`, hash(token), hash("game:"+token)); err != nil {
		t.Fatal(err)
	}
	handler := testServer(t, pool, Config{Origin: "http://localhost", Storage: t.TempDir()}).Handler()
	audio, err := os.ReadFile("../audio/testdata/cbr.mp3")
	if err != nil {
		t.Fatal(err)
	}
	source := "TITLE:Maker Test\nMAKER:Original\nBPM:120\nWAVE:cbr.mp3\n"
	for _, course := range []string{"Hard", "Oni", "Edit"} {
		source += "COURSE:" + course + "\nLEVEL:5\n#START\n1000,\n#END\n"
	}
	post := func(makers, key string, extra ...map[string]string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		tf, _ := form.CreateFormFile("tja", "chart.tja")
		tf.Write([]byte(source))
		af, _ := form.CreateFormFile("audio", "cbr.mp3")
		af.Write(audio)
		if makers != "" {
			form.WriteField("difficultyMakers", makers)
		}
		for _, fields := range extra {
			for name, value := range fields {
				form.WriteField(name, value)
			}
		}
		form.Close()
		r := httptest.NewRequest("POST", "/api/v1/charts", &body)
		r.Header.Set("Content-Type", form.FormDataContentType())
		r.Header.Set("Origin", "http://localhost")
		r.Header.Set("X-CSRF-Token", "csrf")
		r.Header.Set("Idempotency-Key", key)
		r.AddCookie(&http.Cookie{Name: "ourtaiko_session", Value: token})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	decode := func(w *httptest.ResponseRecorder, status int) Chart {
		t.Helper()
		var c Chart
		if w.Code != status || json.Unmarshal(w.Body.Bytes(), &c) != nil {
			t.Fatalf("%d: %s", w.Code, w.Body.String())
		}
		return c
	}
	original := decode(post("", ID()), 201)
	if original.Maker != "Original" {
		t.Fatal(original.Maker)
	}
	for _, d := range original.Difficulties {
		if d.Maker != "Original" {
			t.Fatal(d)
		}
	}
	previewKey := ID()
	custom := map[string]string{"demoStart": "0.1", "demoEnd": "0.3"}
	preview := decode(post("", previewKey, custom), 201)
	if preview.DemoStart != 0.1 || preview.DemoEnd != 0.3 || preview.PreviewPath == "" {
		t.Fatalf("range not saved: %+v", preview)
	}
	if retry := decode(post("", previewKey, custom), 200); retry.ID != preview.ID {
		t.Fatal("range retry created new chart")
	}
	if w := post("", previewKey, map[string]string{"demoStart": "0.1", "demoEnd": "0.4"}); w.Code != 409 {
		t.Fatalf("range conflict: %d %s", w.Code, w.Body.String())
	}
	for _, fields := range []map[string]string{{"demoStart": "-1"}, {"demoStart": "NaN"}, {"demoEnd": "Inf"}, {"demoEnd": ""}, {"demoStart": "0.3", "demoEnd": "0.1"}, {"demoStart": "1200"}, {"demoEnd": "1216"}} {
		if w := post("", ID(), fields); w.Code != 422 {
			t.Fatalf("bad range accepted: %d %s", w.Code, w.Body.String())
		}
	}
	makers := `[{"course":"Edit","maker":"A"},{"course":"Hard","maker":" A "},{"course":"Oni","maker":"B"}]`
	key := ID()
	c := decode(post(makers, key), 201)
	if c.Maker != "A | B" {
		t.Fatal(c.Maker)
	}
	for i, name := range []string{"A", "B", "A"} {
		if c.Difficulties[i].Maker != name {
			t.Fatal(c.Difficulties)
		}
	}
	retry := decode(post(`[{"course":"Hard","maker":"A"},{"course":"Oni","maker":"B"},{"course":"Edit","maker":"A"}]`, key), 200)
	if retry.ID != c.ID {
		t.Fatal("retry created new chart")
	}
	if w := post(strings.ReplaceAll(makers, `"B"`, `"C"`), key); w.Code != 409 {
		t.Fatal("maker change must conflict", w.Code, w.Body.String())
	}
	for _, raw := range []string{`null`, `[]`, `{}`, `[{"course":"Hard","maker":"A"}]`, `[{"course":"Hard","maker":"A"},{"course":"Hard","maker":"B"},{"course":"Edit","maker":"C"}]`, `[{"course":"Hard","maker":"A"},{"course":"Oni","maker":"B"},{"course":"Missing","maker":"C"}]`, `[{"course":"Hard","maker":null},{"course":"Oni","maker":"B"},{"course":"Edit","maker":"C"}]`, strings.Replace(makers, `"B"`, `"B\nC"`, 1), strings.Replace(makers, `"B"`, `"`+strings.Repeat("界", 167)+`"`, 1)} {
		if w := post(raw, ID()); w.Code != 422 {
			t.Fatalf("invalid %s: %d %s", raw, w.Code, w.Body.String())
		}
	}
	blank := decode(post(`[{"course":"Hard","maker":""},{"course":"Oni","maker":" "},{"course":"Edit","maker":""}]`, ID()), 201)
	if blank.Maker != "" {
		t.Fatal("blank should not fall back to original", blank.Maker)
	}
	for _, path := range []string{"/api/v1/charts/" + c.ID, "/api/v1/charts?q=B", "/api/v1/me/charts", "/api/v1/game/categories/variety/charts"} {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(&http.Cookie{Name: "ourtaiko_session", Value: token})
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"maker":"A | B"`) || !strings.Contains(w.Body.String(), `"maker":"B"`) {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM charts c CROSS JOIN LATERAL jsonb_array_elements(c.difficulties) d WHERE c.id=$1 AND d->>'maker' IN ('A','B')`, c.ID).Scan(&count); err != nil || count != 3 {
		t.Fatal(count, err)
	}
}
