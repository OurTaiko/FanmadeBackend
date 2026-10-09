package httpapi

import (
	"context"
	"encoding/json"
	"mime"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"ourtaiko.dev/fanmade/api/internal/objectstore"
	"ourtaiko.dev/fanmade/api/internal/teststore"
)

func TestCDNAvatarAllowlist(t *testing.T) {
	c := &SSOClient{config: SSOConfig{Issuer: "https://sso.example", AvatarBaseURL: "https://cdn.example/sso/avatars"}}
	id, digest := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for _, base := range []string{"https://sso.example/avatars", "https://cdn.example/sso/avatars"} {
		valid := base + "/" + id + "/" + digest + ".webp"
		if got := c.avatarURL(User{ID: id, AvatarURL: valid}); got != valid {
			t.Fatal(got)
		}
		for _, bad := range []string{valid + "?x=1", valid + "#x", strings.Replace(valid, id, strings.Repeat("c", 32), 1), strings.Replace(valid, ".example", ".example.evil", 1), base + "/" + id + "/../" + digest + ".webp"} {
			if got := c.avatarURL(User{ID: id, AvatarURL: bad}); got != "" {
				t.Fatalf("accepted %s", bad)
			}
		}
	}
}
func TestCDNRedirectFilename(t *testing.T) {
	remote := &objectstore.S3{}
	if err := remote.SetPublicBaseURL("https://cdn.example/fanmade"); err != nil {
		t.Fatal(err)
	}
	s := New(nil, Config{Objects: remote})
	w := httptest.NewRecorder()
	if !s.redirectResource(w, httptest.NewRequest("GET", "/download", nil), "archives/abc/download.zip", "日本語の曲.zip") {
		t.Fatal("not redirected")
	}
	target, err := url.Parse(w.Header().Get("Location"))
	if err != nil || w.Code != 302 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Result(), err)
	}
	disposition, params, err := mime.ParseMediaType(target.Query().Get("response-content-disposition"))
	if err != nil || disposition != "attachment" || params["filename"] != "日本語の曲.zip" {
		t.Fatal(params, err)
	}
}
func TestCDNResourcesAndRoutes(t *testing.T) {
	pool := scoreTestDB(t)
	remote := teststore.New(t)
	if err := remote.SetPublicBaseURL("https://cdn.example/fanmade"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,password_hash) VALUES('owner','cdnowner','unused');
 INSERT INTO charts(id,owner_id,title,bpm,duration,wave_filename,difficulties) VALUES('cdn-song','owner','CDN',120,100,'audio.ogg','[{"course":"Oni","level":5,"maker":""}]');
 INSERT INTO chart_resources(chart_id,kind,storage_key,original_filename,sha256,byte_size,media_type) VALUES
 ('cdn-song','tja','objects/song/tja','song.tja',repeat('a',64),12,'application/octet-stream'),
 ('cdn-song','audio','objects/song/audio','audio.ogg',repeat('b',64),20,'audio/ogg'),
 ('cdn-song','archive','archives/song/download.zip','song.zip',repeat('c',64),30,'application/zip'),
 ('cdn-song','cover','covers/song/cover.webp','cover.webp',repeat('d',64),14,'image/webp');`); err != nil {
		t.Fatal(err)
	}
	app := New(pool, Config{Objects: remote})
	handler := app.Handler()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/charts/cdn-song/resources", nil))
	var payload struct {
		URLsExpire bool      `json:"urlsExpire"`
		ExpiresAt  time.Time `json:"expiresAt"`
		Resources  map[string]struct {
			URL     string `json:"url"`
			HeadURL string `json:"headUrl"`
		} `json:"resources"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &payload) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	if payload.URLsExpire || payload.ExpiresAt.Before(time.Now()) || len(payload.Resources) != 4 {
		t.Fatal(payload)
	}
	for _, r := range payload.Resources {
		if r.URL != r.HeadURL || !strings.HasPrefix(r.URL, "https://cdn.example/fanmade/") || strings.Contains(r.URL, "?") {
			t.Fatal(r)
		}
	}
	for _, method := range []string{"GET", "HEAD"} {
		for _, path := range []string{"audio", "tja", "download", "cover"} {
			w = httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(method, "/api/v1/charts/cdn-song/"+path, nil))
			if w.Code != 302 || !strings.HasPrefix(w.Header().Get("Location"), "https://cdn.example/fanmade/") {
				t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
			}
		}
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/charts/cdn-song/cover?v=obsolete", nil))
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
	if _, err := pool.Exec(ctx, `UPDATE charts SET status='deleted' WHERE id='cdn-song'`); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"audio", "tja", "download", "cover", "resources"} {
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/charts/cdn-song/"+path, nil))
		if w.Code != 404 {
			t.Fatalf("removed %s: %d", path, w.Code)
		}
	}
}
