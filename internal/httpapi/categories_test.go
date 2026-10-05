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
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,password_hash) VALUES('89b6ef3a5cb57b6e04f74711d15a8a5f','testd46774d30dd1','unused'),('9b893bc6d9422c93536ff0df503b81e9','test9b893bc6d942','unused');`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,'89b6ef3a5cb57b6e04f74711d15a8a5f','csrf',now()+interval '1 day'),($2,'89b6ef3a5cb57b6e04f74711d15a8a5f','csrf',now()+interval '1 day'),($3,'9b893bc6d9422c93536ff0df503b81e9','csrf',now()+interval '1 day')`, hash(token), hash("game:"+gameToken), hash(otherToken)); err != nil {
		t.Fatal(err)
	}
	handler := testServer(t, pool, Config{Origin: origin, Storage: t.TempDir()}).Handler()
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
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &catalog) != nil || len(catalog.Items) != 6 {
		t.Fatal(w.Body.String())
	}
	wantCatalog := []Category{
		{"game", "Game", "GAME"}, {"virtual-singer", "Virtual Singer", "VOCALOID"},
		{"pop", "Pop", "J-POP"}, {"classic", "Classic", "CLASSICAL"},
		{"variety", "Variety", "VARIETY"}, {"anime", "Anime", "ANIME"},
	}
	if !reflect.DeepEqual(catalog.Items, wantCatalog) {
		t.Fatal("catalog metadata/order changed", catalog)
	}
	for _, raw := range []string{"", "[]", "null"} {
		c := decodeChart(post(raw, ID()), 201)
		if !reflect.DeepEqual(c.CategoryIDs, []string{"variety"}) {
			t.Fatal("default", c.CategoryIDs)
		}
	}
	key := ID()
	c := decodeChart(post(`["pop","anime","game","pop"]`, key), 201)
	if !reflect.DeepEqual(c.CategoryIDs, []string{"anime", "game", "pop"}) {
		t.Fatal(c.CategoryIDs)
	}
	var flags int32
	if err = pool.QueryRow(ctx, `SELECT category_flags FROM charts WHERE id=$1`, c.ID).Scan(&flags); err != nil || flags != 37 {
		t.Fatal("multi-category storage", flags, err)
	}
	retry := decodeChart(post(`["game","pop","anime"]`, key), 200)
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
	var summary struct {
		ChartCount int
		Categories []struct {
			Category
			ChartCount int
		}
	}
	if err = json.Unmarshal(w.Body.Bytes(), &summary); err != nil || summary.ChartCount != 4 || len(summary.Categories) != 6 {
		t.Fatal("server total must count unique charts", w.Body.String(), err)
	}
	wantCounts := map[string]int{"game": 1, "pop": 1, "variety": 3, "classic": 0, "virtual-singer": 0, "anime": 1}
	for _, category := range summary.Categories {
		if category.ID == "anime" && (category.Title != "Anime" || category.Genre != "ANIME") {
			t.Fatal("game Anime metadata", category)
		}
		if category.ChartCount != wantCounts[category.ID] {
			t.Fatal("category count", category)
		}
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
	for _, id := range []string{"game", "pop", "anime"} {
		charts := categoryCharts(id)
		if len(charts) != 1 || charts[0].ID != c.ID {
			t.Fatal(id, charts)
		}
	}
	if len(categoryCharts("classic")) != 0 {
		t.Fatal("empty category")
	}
	for _, route := range []string{"/api/v1/game/bootstrap", "/api/v1/game/categories/game/charts"} {
		guest := call("GET", route, "", "")
		authed := call("GET", route, "", gameToken)
		var guestData, authData map[string]json.RawMessage
		if guest.Code != 200 || authed.Code != 200 || json.Unmarshal(guest.Body.Bytes(), &guestData) != nil || json.Unmarshal(authed.Body.Bytes(), &authData) != nil {
			t.Fatal("guest catalog", route, guest.Code, guest.Body.String())
		}
		delete(guestData, "user")
		delete(authData, "user")
		if !reflect.DeepEqual(guestData, authData) {
			t.Fatal("guest must receive the same public catalog", route, guest.Body.String(), authed.Body.String())
		}
	}
	if guest := decodeChart(call("GET", "/api/v1/charts/"+c.ID, "", ""), 200); guest.ID != c.ID {
		t.Fatal("guest chart detail", guest)
	}
	for _, kind := range []string{"tja", "audio"} {
		w := call("GET", "/api/v1/charts/"+c.ID+"/"+kind, "", "")
		if w.Code != 200 || w.Body.Len() == 0 {
			t.Fatal("guest download", kind, w.Code, w.Body.String())
		}
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
	// An existing score must remain attached to the same song after reclassification.
	if _, err = pool.Exec(ctx, `INSERT INTO scores(id,user_id,song_id,difficulty,good,ok,bad,score,drumroll,max_combo,payload_digest) VALUES('score','89b6ef3a5cb57b6e04f74711d15a8a5f',$1,'Oni',1,0,0,1000,0,1,repeat('a',64))`, c.ID); err != nil {
		t.Fatal(err)
	}
	after = decodeChart(call("PATCH", path, `{"categoryIds":["classic","virtual-singer"]}`, token), 200)
	if err = pool.QueryRow(ctx, `SELECT category_flags FROM charts WHERE id=$1`, c.ID).Scan(&flags); err != nil || flags != 10 {
		t.Fatal("category edit must replace, not accumulate bits", flags, err)
	}
	if after.ID != c.ID || after.TJAHash != c.TJAHash || after.AudioHash != c.AudioHash {
		t.Fatal("classification changed content identity")
	}
	if len(categoryCharts("game")) != 0 || len(categoryCharts("pop")) != 0 || len(categoryCharts("anime")) != 0 || len(categoryCharts("classic")) != 1 {
		t.Fatal("membership replacement failed")
	}
	after = decodeChart(call("PATCH", path, `{"title":"Renamed"}`, token), 200)
	if !reflect.DeepEqual(after.CategoryIDs, []string{"classic", "virtual-singer"}) {
		t.Fatal("omitted PATCH categories reset selection")
	}
	after = decodeChart(call("PATCH", path, `{"categoryIds":["anime"]}`, token), 200)
	if !reflect.DeepEqual(after.CategoryIDs, []string{"anime"}) || len(categoryCharts("anime")) != 1 || len(categoryCharts("classic")) != 0 {
		t.Fatal("editing into Anime failed", after.CategoryIDs)
	}
	if after.ID != c.ID || after.TJAHash != c.TJAHash || after.AudioHash != c.AudioHash {
		t.Fatal("Anime classification changed content identity")
	}
	after = decodeChart(call("PATCH", path, `{"categoryIds":[]}`, token), 200)
	if !reflect.DeepEqual(after.CategoryIDs, []string{"variety"}) {
		t.Fatal("empty PATCH default")
	}
	var points int
	if err = pool.QueryRow(ctx, `SELECT score FROM scores WHERE id='score' AND song_id=$1`, c.ID).Scan(&points); err != nil || points != 1000 {
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
	w = call("GET", "/api/v1/game/bootstrap", "", gameToken)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &summary) != nil || summary.ChartCount != 3 {
		t.Fatal("deleted chart counted", w.Body.String())
	}
	for _, category := range summary.Categories {
		if category.ChartCount != len(categoryCharts(category.ID)) {
			t.Fatal("count differs from refreshed list", category)
		}
	}
}
