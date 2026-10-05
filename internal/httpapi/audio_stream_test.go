package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ourtaiko.dev/fanmade/api/internal/objectstore"
	"ourtaiko.dev/fanmade/api/internal/teststore"
	"ourtaiko.dev/fanmade/api/internal/tja"
)

func TestAudioPreviewWindow(t *testing.T) {
	for _, tc := range []struct{ duration, start, wantStart, wantLength float64 }{
		{120, 30, 30, 15}, {12, 0, 0, 12}, {120, 115, 115, 5},
		{120, 120, 0, 15}, {120, -1, 0, 15}, {120, math.NaN(), 0, 15},
		{120, math.Inf(1), 0, 15},
	} {
		c := Chart{ID: "song", VersionID: "version", AudioName: "track.mp3", Duration: tc.duration, Metadata: tja.Metadata{DemoStart: tc.start}}
		p := audioPreview(c)
		if p == nil || p.StartSeconds != tc.wantStart || p.DurationSeconds != tc.wantLength || p.ContentType != "audio/mpeg" || p.URL != "/api/v1/charts/song/versions/version/audio" {
			t.Fatalf("window %+v: %+v", tc, p)
		}
	}
	for _, duration := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if p := audioPreview(Chart{Duration: duration}); p != nil {
			t.Fatalf("invalid duration must not advertise a preview: %+v", p)
		}
	}
}

func TestAudioStreamHTTP(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(fmt.Sprint("s3=", remote), func(t *testing.T) { testAudioStreamHTTP(t, remote) })
	}
}
func testAudioStreamHTTP(t *testing.T, remote bool) {
	pool := scoreTestDB(t)
	ctx := context.Background()
	storage := t.TempDir()
	// No SSO configuration: streaming must never call nickname/auth services.
	cfg := Config{Storage: storage}
	if remote {
		cfg.Objects = teststore.New(t)
	}
	handler := New(pool, cfg).Handler()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO users(id,username,password_hash) VALUES('89b6ef3a5cb57b6e04f74711d15a8a5f','audioowner','unused')`)
	app := testServer(t, pool, cfg)
	// Simulate legacy unsupported rows retained by migration 012.
	mustExec(`ALTER TABLE difficulties DROP CONSTRAINT difficulties_course_check`)
	for _, filename := range []string{"cbr.mp3", "vorbis.ogg"} {
		t.Run(filename, func(t *testing.T) {
			data, err := os.ReadFile("../audio/testdata/" + filename)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(storage, filename), data, 0600); err != nil {
				t.Fatal(err)
			}
			if remote {
				teststore.Seed(t, cfg.Objects.(*objectstore.S3), filename, data)
			}
			song, version, audioID, tjaID := ID(), ID(), ID(), ID()
			digest := hash(string(data))
			mustExec(`INSERT INTO files(id,storage_key,original_filename,sha256,byte_size,media_type) VALUES($1,$2,$2,$3,$4,'audio/test'),($5,$5,'a.tja',repeat('a',64),1,'application/octet-stream')`, audioID, filename, digest, len(data), tjaID)
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err = tx.Exec(ctx, `INSERT INTO charts(id,owner_id,current_version_id) VALUES($1,'89b6ef3a5cb57b6e04f74711d15a8a5f',$2)`, song, version); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, `INSERT INTO chart_versions(id,chart_id,version_number,title,bpm,duration,demo_start,encoding,wave_filename,tja_file_id,audio_file_id,validation_version) VALUES($1,$2,1,'Stream',120,120,30,'utf-8',$3,$4,$5,'test')`, version, song, filename, tjaID, audioID); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			mustExec(`INSERT INTO difficulties(version_id,block_index,course,level,player,style) VALUES($1,0,'Oni',5,'','Single')`, version)
			path := "/api/v1/charts/" + song + "/versions/" + version + "/audio"
			etag := `"` + digest + `"`
			media := "audio/mpeg"
			if filename == "vorbis.ogg" {
				media = "audio/ogg"
			}
			call := func(method, target string, headers map[string]string) *httptest.ResponseRecorder {
				t.Helper()
				r := httptest.NewRequest(method, target, nil)
				for k, v := range headers {
					r.Header.Set(k, v)
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			for _, tc := range []struct {
				name, method, byteRange, ifRange string
				status                           int
				body                             []byte
				contentRange                     string
			}{
				{"full", "GET", "", "", 200, data, ""},
				{"head", "HEAD", "", "", 200, nil, ""},
				{"prefix", "GET", "bytes=0-3", "", 206, data[:4], fmt.Sprintf("bytes 0-3/%d", len(data))},
				{"suffix", "GET", "bytes=-4", "", 206, data[len(data)-4:], fmt.Sprintf("bytes %d-%d/%d", len(data)-4, len(data)-1, len(data))},
				{"open", "GET", "bytes=4-", "", 206, data[4:], fmt.Sprintf("bytes 4-%d/%d", len(data)-1, len(data))},
				{"if-range-match", "GET", "bytes=0-3", etag, 206, data[:4], fmt.Sprintf("bytes 0-3/%d", len(data))},
				{"if-range-stale", "GET", "bytes=0-3", `"stale"`, 200, data, ""},
			} {
				t.Run(tc.name, func(t *testing.T) {
					w := call(tc.method, path, map[string]string{"Range": tc.byteRange, "If-Range": tc.ifRange})
					length := len(tc.body)
					if tc.method == "HEAD" {
						length = len(data)
					}
					if w.Code != tc.status || !bytes.Equal(w.Body.Bytes(), tc.body) || w.Header().Get("Content-Length") != fmt.Sprint(length) || w.Header().Get("Content-Range") != tc.contentRange {
						t.Fatalf("response: %d %v, body length %d", w.Code, w.Header(), w.Body.Len())
					}
					if w.Header().Get("Accept-Ranges") != "bytes" || w.Header().Get("Content-Type") != media || w.Header().Get("ETag") != etag || w.Header().Get("Cache-Control") != "public, no-cache, no-transform" || w.Header().Get("X-Accel-Buffering") != "no" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "inline") {
						t.Fatal("stream headers", w.Header())
					}
				})
			}
			for _, method := range []string{"GET", "HEAD"} {
				w := call(method, path, map[string]string{"If-None-Match": etag})
				if w.Code != 304 || w.Body.Len() != 0 {
					t.Fatal("revalidation", w.Code)
				}
			}
			w := call("GET", path, map[string]string{"Range": fmt.Sprintf("bytes=%d-", len(data))})
			if w.Code != 416 || w.Header().Get("Content-Range") != fmt.Sprintf("bytes */%d", len(data)) {
				t.Fatal("unsatisfiable range", w.Code, w.Header())
			}
			w = call("GET", path, map[string]string{"Range": "bytes=invalid"})
			if w.Code != 416 {
				t.Fatal("invalid range", w.Code)
			}
			w = call("GET", path, map[string]string{"Range": "bytes=0-3,8-11"})
			_, params, err := mime.ParseMediaType(w.Header().Get("Content-Type"))
			if w.Code != 206 || err != nil {
				t.Fatal("multipart range", w.Code, err)
			}
			parts := multipart.NewReader(w.Body, params["boundary"])
			for _, start := range []int{0, 8} {
				part, err := parts.NextPart()
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(part)
				if err != nil || !bytes.Equal(body, data[start:start+4]) {
					t.Fatal("multipart bytes", err)
				}
			}
			if _, err := parts.NextPart(); err != io.EOF {
				t.Fatal("unexpected extra part", err)
			}
			// The advertised URL also works after loading a normal chart response.
			chart, err := app.chart(ctx, song)
			if err != nil || chart.AudioPreview == nil || chart.AudioPreview.URL != path || chart.AudioPreview.StartSeconds != 30 || chart.AudioPreview.DurationSeconds != 15 || chart.AudioHash != digest || chart.AudioSize != int64(len(data)) {
				t.Fatalf("chart preview: %+v, %v", chart, err)
			}
			w = call("GET", "/api/v1/game/bootstrap", nil)
			var bootstrap struct {
				AudioPreviewVersion int `json:"audioPreviewVersion"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &bootstrap) != nil || bootstrap.AudioPreviewVersion != 1 {
				t.Fatal("missing capability", w.Code, w.Body.String())
			}
			w = call("GET", strings.Replace(path, version, ID(), 1), nil)
			if w.Code != 404 || !strings.Contains(w.Body.String(), "VERSION_NOT_FOUND") {
				t.Fatal("stale version", w.Code)
			}
			mustExec(`UPDATE difficulties SET course='Tower' WHERE version_id=$1`, version)
			if w = call("GET", path, nil); w.Code != 404 {
				t.Fatal("unsupported chart served", w.Code)
			}
			mustExec(`UPDATE difficulties SET course='Oni' WHERE version_id=$1`, version)
			mustExec(`UPDATE charts SET status='deleted' WHERE id=$1`, song)
			if w = call("GET", path, map[string]string{"If-None-Match": etag}); w.Code != 404 {
				t.Fatal("deleted chart served/revalidated", w.Code)
			}
		})
	}
}
