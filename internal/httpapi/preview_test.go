package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"ourtaiko.dev/fanmade/api/internal/objectstore"
	"ourtaiko.dev/fanmade/api/internal/teststore"
)

func TestPreviewLifecycle(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(fmt.Sprint("s3=", remote), func(t *testing.T) {
			ctx := context.Background()
			pool := scoreTestDB(t)
			const owner = "d46774d30dd13b92d9e536808da468a4"
			cookie := strings.Repeat("1", 64)
			if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,password_hash) VALUES($1,'author','unused')`, owner); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,$2,'csrf',now()+interval '1 day')`, hash(cookie), owner); err != nil {
				t.Fatal(err)
			}
			cfg := Config{Storage: t.TempDir(), Origin: "http://localhost"}
			if remote {
				cfg.Objects = teststore.New(t)
			}
			app := testServer(t, pool, cfg)
			source, err := os.ReadFile("../audio/testdata/vorbis.ogg")
			if err != nil {
				t.Fatal(err)
			}
			file, err := os.Open("../audio/testdata/vorbis.ogg")
			if err != nil {
				t.Fatal(err)
			}
			err = app.Config.Objects.Put(ctx, "audio", file, int64(len(source)), "audio/ogg", hash(string(source)))
			file.Close()
			if err != nil {
				t.Fatal(err)
			}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err = tx.Exec(ctx, `INSERT INTO charts(id,owner_id,title,bpm,duration,encoding,wave_filename,demo_start,difficulties) VALUES('song',$1,'Test',120,0.5,'utf-8','audio.ogg',0.1,'[{"course":"Oni","level":5,"maker":"A"}]')`, owner); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, `INSERT INTO chart_resources(chart_id,kind,storage_key,original_filename,sha256,byte_size,media_type) VALUES('song','audio','audio','audio.ogg',$1,$2,'audio/ogg'),('song','tja','tja','song.tja',repeat('a',64),1,'application/octet-stream')`, hash(string(source)), len(source)); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = app.BackfillPreviews(ctx); err != nil {
				t.Fatal(err)
			}
			var key string
			if err = pool.QueryRow(ctx, `SELECT storage_key FROM chart_resources WHERE chart_id='song' AND kind='preview'`).Scan(&key); err != nil {
				t.Fatal(err)
			}
			c, err := app.chart(ctx, "song")
			if err != nil || c.DemoEnd != 15.1 || !strings.HasSuffix(c.PreviewPath, "/preview.ogg") {
				t.Fatalf("chart=%+v err=%v", c, err)
			}
			if remote && c.PreviewPath != cfg.Objects.(*objectstore.S3).Prefix+key {
				t.Fatal("incorrect bucket path", c.PreviewPath)
			}
			if err = app.BackfillPreviews(ctx); err != nil {
				t.Fatal(err)
			}
			var unchanged string
			pool.QueryRow(ctx, `SELECT storage_key FROM chart_resources WHERE chart_id='song' AND kind='preview'`).Scan(&unchanged)
			if unchanged != key {
				t.Fatal("backfill replaced an existing preview")
			}
			call := func(body, token, csrf string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("PATCH", "/api/v1/charts/song", strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Origin", "http://localhost")
				r.Header.Set("X-CSRF-Token", csrf)
				if token != "" {
					r.AddCookie(&http.Cookie{Name: "ourtaiko_session", Value: token})
				}
				w := httptest.NewRecorder()
				app.Handler().ServeHTTP(w, r)
				return w
			}
			if w := call(`{"demoEnd":0.3}`, "", "csrf"); w.Code != 401 {
				t.Fatal(w.Code, w.Body.String())
			}
			if w := call(`{"demoEnd":0.3}`, cookie, ""); w.Code != 403 {
				t.Fatal(w.Code, w.Body.String())
			}
			for _, body := range []string{`{"demoStart":-1}`, `{"demoEnd":0.1}`, `{"demoStart":0.5}`, `{"demoEnd":null}`, `{"demoStart":"0.2"}`} {
				if w := call(body, cookie, "csrf"); w.Code != 422 {
					t.Fatal(w.Code, w.Body.String())
				}
			}
			w := call(`{"demoStart":0.2,"demoEnd":0.4}`, cookie, "csrf")
			var updated Chart
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &updated) != nil || updated.DemoEnd != 0.4 || updated.DemoStart != 0.2 || updated.PreviewPath == c.PreviewPath {
				t.Fatal(w.Code, w.Body.String())
			}
			if updated.AudioHash != c.AudioHash || updated.TJAHash != c.TJAHash {
				t.Fatal("source resources changed")
			}
			app.cleanupReplacedFiles()
			if _, err = app.Config.Objects.Open(ctx, key); !os.IsNotExist(err) {
				t.Fatal("old preview still exists", err)
			}
			// Failure while retrieving the source must leave both range and preview unchanged.
			if err = app.Config.Objects.Delete(ctx, "audio"); err != nil {
				t.Fatal(err)
			}
			if w = call(`{"demoEnd":0.45}`, cookie, "csrf"); w.Code != 503 {
				t.Fatal(w.Code, w.Body.String())
			}
			after, err := app.chart(ctx, "song")
			if err != nil || after.DemoEnd != updated.DemoEnd || after.PreviewPath != updated.PreviewPath {
				t.Fatal("failed save changed live preview", err)
			}
			// Lists expose the current preview location; the full audio endpoint stays separate.
			w = httptest.NewRecorder()
			app.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/charts", nil))
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"previewPath"`) {
				t.Fatal(w.Code, w.Body.String())
			}
			var currentKey string
			pool.QueryRow(ctx, `SELECT storage_key FROM chart_resources WHERE chart_id='song' AND kind='preview'`).Scan(&currentKey)
			object, err := app.Config.Objects.Open(ctx, currentKey)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(object)
			object.Close()
			if err != nil || string(data[:4]) != "OggS" {
				t.Fatal("invalid stored preview", err)
			}
		})
	}
}
