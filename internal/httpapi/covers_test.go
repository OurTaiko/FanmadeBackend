package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"ourtaiko.dev/fanmade/api/internal/cover"
)

func TestCoverLifecycle(t *testing.T) {
	t.Run("jpg-png", func(t *testing.T) { testCoverLifecycle(t, false) })
	t.Run("webp", func(t *testing.T) { testCoverLifecycle(t, true) })
}

func testCoverLifecycle(t *testing.T, useWebP bool) {
	// scoreTestDB creates a unique score_api_test_<timestamp> schema, configures
	// search_path on every connection, and registers DROP SCHEMA in t.Cleanup.
	pool := scoreTestDB(t)
	ctx := context.Background()
	var schema string
	if err := pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil || !strings.HasPrefix(schema, "score_api_test_") {
		t.Fatalf("refuse non-isolated test schema: %q %v", schema, err)
	}
	_, err := pool.Exec(ctx, `INSERT INTO users(id,username,password_hash,is_admin) VALUES(repeat('1',32),'owner','unused',false),(repeat('2',32),'other','unused',false),(repeat('3',32),'admin','unused',true)`)
	if err != nil {
		t.Fatal(err)
	}
	cookies := map[string]string{}
	for index, id := range []string{"owner", "other", "admin"} {
		cookies[id] = ID() + ID()
		if _, err = pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,$2,'csrf',now()+interval '1 day')`, hash(cookies[id]), strings.Repeat(string(rune('1'+index)), 32)); err != nil {
			t.Fatal(err)
		}
	}
	app := testServer(t, pool, Config{Origin: "http://localhost", Storage: t.TempDir()})
	handler := app.Handler()
	type part struct {
		field, name string
		data        []byte
	}
	send := func(method, path, user, csrf, key string, parts ...part) *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		for _, p := range parts {
			if p.name == "" {
				_ = form.WriteField(p.field, string(p.data))
			} else {
				out, e := form.CreateFormFile(p.field, p.name)
				if e != nil {
					t.Fatal(e)
				}
				_, _ = out.Write(p.data)
			}
		}
		_ = form.Close()
		r := httptest.NewRequest(method, path, &body)
		r.Header.Set("Origin", "http://localhost")
		r.Header.Set("Content-Type", form.FormDataContentType())
		r.Header.Set("X-CSRF-Token", csrf)
		r.Header.Set("Idempotency-Key", key)
		if user != "" {
			r.AddCookie(&http.Cookie{Name: "ourtaiko_session", Value: cookies[user]})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	status := func(w *httptest.ResponseRecorder, want int) {
		t.Helper()
		if w.Code != want {
			t.Fatalf("status %d want %d: %s", w.Code, want, w.Body.String())
		}
	}
	decode := func(w *httptest.ResponseRecorder) Chart {
		t.Helper()
		var c Chart
		if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	count := func(query string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	picture := func(jpg bool, c color.NRGBA) []byte {
		img := image.NewNRGBA(image.Rect(0, 0, 40, 30))
		for y := 0; y < 30; y++ {
			for x := 0; x < 40; x++ {
				img.SetNRGBA(x, y, c)
			}
		}
		var b bytes.Buffer
		if jpg {
			_ = jpeg.Encode(&b, img, nil)
		} else {
			_ = png.Encode(&b, img)
		}
		return b.Bytes()
	}
	pngImage := picture(false, color.NRGBA{R: 255, A: 180})
	jpgImage := picture(true, color.NRGBA{B: 255, A: 255})
	audio, err := os.ReadFile("../audio/testdata/vorbis.ogg")
	if err != nil {
		t.Fatal(err)
	}
	parts := []part{{"tja", "chart.tja", []byte("TITLE:Cover test\nBPM:120\nWAVE:music.ogg\nCOURSE:Oni\nLEVEL:5\n#START\n1000,\n#END\n")}, {"audio", "music.ogg", audio}, {"cover", "cover.png", pngImage}}
	if useWebP {
		data, err := cover.Encode(ctx, "cover.png", pngImage)
		if err != nil {
			t.Fatal(err)
		}
		parts[2] = part{"cover", "cover.webp", data}
	}
	key := ID()
	w := send("POST", "/api/v1/charts", "owner", "csrf", key, parts...)
	status(w, 201)
	original := decode(w)
	if original.CoverHash == "" {
		t.Fatal("upload response missing cover hash")
	}
	path := "/api/v1/charts/" + original.ID + "/cover"
	get := func(url, etag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", url, nil)
		r.Header.Set("If-None-Match", etag)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w = get(path, "")
	status(w, 200)
	initialBytes := append([]byte(nil), w.Body.Bytes()...)
	if w.Header().Get("Content-Type") != "image/webp" || len(initialBytes) < 12 || string(initialBytes[8:12]) != "WEBP" {
		t.Fatal("not WebP")
	}
	if hash(string(initialBytes)) != original.CoverHash {
		t.Fatal("incorrect hash")
	}
	status(get(path, w.Header().Get("ETag")), 304)
	var firstID string
	if err = pool.QueryRow(ctx, `SELECT id FROM chart_covers WHERE chart_id=$1`, original.ID).Scan(&firstID); err != nil {
		t.Fatal(err)
	}
	status(send("POST", "/api/v1/charts", "owner", "csrf", key, parts...), 200)
	if count(`SELECT count(*) FROM chart_covers`) != 1 || count(`SELECT count(*) FROM files`) != 2 {
		t.Fatal("retry duplicated records")
	}
	changed := append([]part(nil), parts...)
	changed[2] = part{"cover", "new.jpg", jpgImage}
	if useWebP {
		data, err := cover.Encode(ctx, "new.jpg", jpgImage)
		if err != nil {
			t.Fatal(err)
		}
		changed[2] = part{"cover", "new.WEBP", data}
	}
	status(send("POST", "/api/v1/charts", "owner", "csrf", key, changed...), 409)
	for _, tc := range []struct {
		user, csrf string
		status     int
	}{{"", "csrf", 401}, {"owner", "wrong", 403}, {"other", "csrf", 403}, {"admin", "csrf", 403}} {
		status(send("PUT", path, tc.user, tc.csrf, "", changed[2]), tc.status)
	}
	for _, tc := range []struct {
		p      part
		status int
	}{{part{"cover", "fake.webp", pngImage}, 422}, {part{"cover", "bad.webp", []byte("bad")}, 422}, {part{"cover", "fake.jpg", pngImage}, 422}, {part{"cover", "bad.png", []byte("bad")}, 422}, {part{"cover", "file.svg", pngImage}, 400}, {part{"cover", "huge.png", make([]byte, cover.MaxBytes+1)}, 413}} {
		status(send("PUT", path, "owner", "csrf", "", tc.p), tc.status)
	}
	status(send("PUT", path, "owner", "csrf", "", changed[2], changed[2]), 400)
	status(send("PUT", path, "owner", "csrf", "", changed[2], part{"unexpected", "", []byte("value")}), 400)
	if !bytes.Equal(get(path, "").Body.Bytes(), initialBytes) {
		t.Fatal("validation changed cover")
	}
	// This trigger exists only inside the temporary schema. Force failure after
	// DELETE to prove that the transaction restores the previous row and bytes.
	_, err = pool.Exec(ctx, `CREATE FUNCTION reject_cover() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test write failure'; END $$; CREATE TRIGGER reject_cover BEFORE INSERT ON chart_covers FOR EACH ROW EXECUTE FUNCTION reject_cover()`)
	if err != nil {
		t.Fatal(err)
	}
	status(send("PUT", path, "owner", "csrf", "", changed[2]), 503)
	if !bytes.Equal(get(path, "").Body.Bytes(), initialBytes) {
		t.Fatal("transaction lost old cover")
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER reject_cover ON chart_covers; DROP FUNCTION reject_cover()`); err != nil {
		t.Fatal(err)
	}
	w = send("PUT", path, "owner", "csrf", "", changed[2])
	status(w, 200)
	var result struct {
		CoverHash string `json:"coverHash"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if result.CoverHash == "" || result.CoverHash == original.CoverHash {
		t.Fatal("hash unchanged")
	}
	var secondID string
	if err = pool.QueryRow(ctx, `SELECT id FROM chart_covers WHERE chart_id=$1`, original.ID).Scan(&secondID); err != nil {
		t.Fatal(err)
	}
	if firstID == secondID || count(`SELECT count(*) FROM chart_covers`) != 1 {
		t.Fatal("old cover row retained")
	}
	status(get(path+"?v="+original.CoverHash, ""), 404)
	w = get(path+"?v="+result.CoverHash, "")
	status(w, 200)
	if bytes.Equal(w.Body.Bytes(), initialBytes) {
		t.Fatal("serving old cover")
	}
	detail := decode(get("/api/v1/charts/"+original.ID, ""))
	if detail.CoverHash != result.CoverHash || detail.VersionID != original.VersionID || detail.AudioHash != original.AudioHash || detail.TJAHash != original.TJAHash {
		t.Fatal("changed chart resources")
	}
	w = get("/api/v1/charts", "")
	status(w, 200)
	if !strings.Contains(w.Body.String(), result.CoverHash) {
		t.Fatal("list missing cover")
	}
	w = get("/api/v1/game/categories/variety/charts", "")
	status(w, 200)
	if strings.Contains(w.Body.String(), "coverHash") {
		t.Fatal("changed game response")
	}
	replacement := []part{parts[0], {"expectedVersionId", "", []byte(original.VersionID)}, {"confirmReset", "", []byte("true")}}
	w = send("PUT", "/api/v1/charts/"+original.ID+"/files", "owner", "csrf", ID(), replacement...)
	status(w, 200)
	if decode(w).CoverHash != result.CoverHash {
		t.Fatal("file replacement lost cover")
	}
	if count(`SELECT count(*) FROM files`) != 2 || count(`SELECT count(*) FROM chart_covers`) != 1 {
		t.Fatal("unexpected file records")
	}
	status(send("DELETE", "/api/v1/charts/"+original.ID, "owner", "csrf", ""), 200)
	status(get(path, ""), 404)
	status(send("PUT", path, "owner", "csrf", "", changed[2]), 404)
	if count(`SELECT count(*) FROM chart_covers`) != 0 {
		t.Fatal("deleted song retained cover")
	}
	w = send("POST", "/api/v1/charts", "owner", "csrf", ID(), parts[:2]...)
	status(w, 201)
	if decode(w).CoverHash != "" {
		t.Fatal("unexpected cover")
	}
	status(get("/api/v1/charts/"+decode(w).ID+"/cover", ""), 404)
	// Two simultaneous owner changes must leave exactly one complete cover.
	optionalID := decode(w).ID
	responses := make(chan *httptest.ResponseRecorder, 2)
	for _, image := range []part{parts[2], changed[2]} {
		go func(image part) {
			responses <- send("PUT", "/api/v1/charts/"+optionalID+"/cover", "owner", "csrf", "", image)
		}(image)
	}
	for range 2 {
		status(<-responses, 200)
	}
	if count(`SELECT count(*) FROM chart_covers`) != 1 {
		t.Fatal("concurrent replacement retained multiple images")
	}
	w = get("/api/v1/charts/"+optionalID+"/cover", "")
	status(w, 200)
	var storedHash string
	if err := pool.QueryRow(ctx, `SELECT sha256 FROM chart_covers WHERE chart_id=$1`, optionalID).Scan(&storedHash); err != nil || storedHash != hash(w.Body.String()) {
		t.Fatal("concurrent replacement corrupted cover", err)
	}
	status(send("DELETE", "/api/v1/charts/"+optionalID, "owner", "csrf", ""), 200)
	invalid := append([]part(nil), parts...)
	invalid[0] = part{"tja", "broken.tja", []byte("not a chart")}
	status(send("POST", "/api/v1/charts", "owner", "csrf", ID(), invalid...), 422)
	if count(`SELECT count(*) FROM charts`) != 2 || count(`SELECT count(*) FROM chart_covers`) != 0 {
		t.Fatal("invalid upload persisted data")
	}
}
