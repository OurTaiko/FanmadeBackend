package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestCategoriesFlow(t *testing.T) {
	pool := scoreTestDB(t)
	ctx := context.Background()
	const origin = "http://localhost"
	token := strings.Repeat("a", 64)
	gameToken := strings.Repeat("b", 64)
	otherToken := strings.Repeat("c", 64)
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,password_hash) VALUES('u','owner','unused'),('other','other','unused');`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,'u','csrf',now()+interval '1 day'),($2,'u','csrf',now()+interval '1 day'),($3,'other','csrf',now()+interval '1 day')`, hash(token), hash("game:"+gameToken), hash(otherToken)); err != nil {
		t.Fatal(err)
	}
	handler := New(pool, Config{Origin: origin, Storage: t.TempDir()}).Handler()
	call := func(method, path, body, actor string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if strings.Contains(path, "/game/") {
			if actor != "" {
				r.Header.Set("Authorization", "Bearer "+actor)
			}
		} else {
			r.Header.Set("Origin", origin)
			r.Header.Set("X-CSRF-Token", "csrf")
			r.AddCookie(&http.Cookie{Name: "ourtaiko_session", Value: actor})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	audio, err := os.ReadFile("../audio/testdata/cbr.mp3")
	if err != nil {
		t.Fatal(err)
	}
	post := func(selection, key string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		tf, _ := form.CreateFormFile("tja", "chart.tja")
		tf.Write([]byte("TITLE:Category Test\nBPM:120\nWAVE:cbr.mp3\nCOURSE:Oni\nLEVEL:5\n#START\n1000,\n#END\n"))
		af, _ := form.CreateFormFile("audio", "cbr.mp3")
		af.Write(audio)
		if selection != "" {
			form.WriteField("categoryIds", selection)
		}
		form.Close()
		r := httptest.NewRequest("POST", "/api/v1/charts", &body)
		r.Header.Set("Content-Type", form.FormDataContentType())
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", "csrf")
		r.Header.Set("Idempotency-Key", key)
		r.AddCookie(&http.Cookie{Name: "ourtaiko_session", Value: token})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	decodeChart := func(w *httptest.ResponseRecorder, status int) Chart {
		t.Helper()
		var c Chart
		if w.Code != status || json.Unmarshal(w.Body.Bytes(), &c) != nil {
			t.Fatalf("wanted %d: %d %s", status, w.Code, w.Body.String())
		}
		return c
	}
	w := call("GET", "/api/v1/categories", "", "")
	var catalog struct{ Items []Category }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &catalog) != nil || len(catalog.Items) != 5 {
		t.Fatal(w.Body.String())
	}
	for i, id := range []string{"game", "virtual-singer", "pop", "classic", "variety"} {
		if catalog.Items[i].ID != id {
			t.Fatal(catalog)
		}
	}
	for _, raw := range []string{"", "[]", "null"} {
		c := decodeChart(post(raw, ID()), 201)
		if !reflect.DeepEqual(c.CategoryIDs, []string{"variety"}) {
			t.Fatal("default", c.CategoryIDs)
		}
	}
	key := ID()
	c := decodeChart(post(`["pop","game","pop"]`, key), 201)
	if !reflect.DeepEqual(c.CategoryIDs, []string{"game", "pop"}) {
		t.Fatal(c.CategoryIDs)
	}
	retry := decodeChart(post(`["game","pop"]`, key), 200)
	if retry.ID != c.ID {
		t.Fatal("duplicate upload")
	}
	if w = post(`["classic"]`, key); w.Code != 409 {
		t.Fatal("category change reused upload key", w.Code, w.Body.String())
	}
	for _, raw := range []string{`"game"`, `["missing"]`, `[null]`, `[1]`, `{}`} {
		if w = post(raw, ID()); w.Code != 422 {
			t.Fatal("invalid category accepted", raw, w.Code, w.Body.String())
		}
	}
	w = call("GET", "/api/v1/game/bootstrap", "", gameToken)
	var bootstrap map[string]json.RawMessage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &bootstrap) != nil || bootstrap["charts"] != nil || bootstrap["categories"] == nil {
		t.Fatal("bootstrap must not enumerate charts", w.Body.String())
	}
	categoryCharts := func(id string) []Chart {
		t.Helper()
		w := call("GET", "/api/v1/game/categories/"+id+"/charts", "", gameToken)
		var result struct {
			CategoryID string
			Charts     []Chart
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.CategoryID != id {
			t.Fatal(w.Code, w.Body.String())
		}
		return result.Charts
	}
	for _, id := range []string{"game", "pop"} {
		charts := categoryCharts(id)
		if len(charts) != 1 || charts[0].ID != c.ID {
			t.Fatal(id, charts)
		}
	}
	if len(categoryCharts("classic")) != 0 {
		t.Fatal("empty category")
	}
	if w = call("GET", "/api/v1/game/categories/game/charts", "", ""); w.Code != 401 {
		t.Fatal("anonymous game categories")
	}
	if w = call("GET", "/api/v1/game/categories/missing/charts", "", gameToken); w.Code != 404 {
		t.Fatal("unknown category")
	}
	path := "/api/v1/charts/" + c.ID
	if w = call("PATCH", path, `{"categoryIds":["classic"]}`, otherToken); w.Code != 403 {
		t.Fatal("nonowner changed categories")
	}
	if w = call("PATCH", path, `{"title":"Should roll back","categoryIds":["missing"]}`, token); w.Code != 422 {
		t.Fatal("invalid category patch")
	}
	after := decodeChart(call("GET", path, "", ""), 200)
	if after.Title != c.Title || !reflect.DeepEqual(after.CategoryIDs, c.CategoryIDs) {
		t.Fatal("partial invalid patch")
	}
	// An existing score must remain attached to the same chart/version after reclassification.
	if _, err = pool.Exec(ctx, `INSERT INTO scores(id,user_id,song_id,version_id,block_index,difficulty,good,ok,bad,score,drumroll,max_combo,payload_digest) VALUES('score','u',$1,$2,0,'Oni',1,0,0,1000,0,1,repeat('a',64))`, c.ID, c.VersionID); err != nil {
		t.Fatal(err)
	}
	after = decodeChart(call("PATCH", path, `{"categoryIds":["classic","virtual-singer"]}`, token), 200)
	if after.VersionID != c.VersionID || after.TJAHash != c.TJAHash || after.AudioHash != c.AudioHash {
		t.Fatal("classification changed content identity")
	}
	if len(categoryCharts("game")) != 0 || len(categoryCharts("pop")) != 0 || len(categoryCharts("classic")) != 1 {
		t.Fatal("membership replacement failed")
	}
	after = decodeChart(call("PATCH", path, `{"title":"Renamed"}`, token), 200)
	if !reflect.DeepEqual(after.CategoryIDs, []string{"classic", "virtual-singer"}) {
		t.Fatal("omitted PATCH categories reset selection")
	}
	after = decodeChart(call("PATCH", path, `{"categoryIds":[]}`, token), 200)
	if !reflect.DeepEqual(after.CategoryIDs, []string{"variety"}) {
		t.Fatal("empty PATCH default")
	}
	var points int
	if err = pool.QueryRow(ctx, `SELECT score FROM scores WHERE id='score' AND song_id=$1 AND version_id=$2`, c.ID, c.VersionID).Scan(&points); err != nil || points != 1000 {
		t.Fatal("lost score", err)
	}
	if w = call("DELETE", path, "", token); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, chart := range categoryCharts("variety") {
		if chart.ID == c.ID {
			t.Fatal("deleted chart leaked")
		}
	}
}
