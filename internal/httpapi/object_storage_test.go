package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"ourtaiko.dev/fanmade/api/internal/teststore"
	"testing"
)

func verifyResourceLinks(t *testing.T, h http.Handler, chartID string, audio []byte) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/charts/"+chartID+"/resources", nil))
	var payload struct {
		ChartID string `json:"chartId"`

		Resources map[string]struct {
			URL     string `json:"url"`
			HeadURL string `json:"headUrl"`
			SHA     string `json:"sha256"`
			Size    int64  `json:"size"`
		}
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &payload) != nil {
		t.Fatal("resources", w.Code, w.Body.String())
	}
	if payload.ChartID != chartID {
		t.Fatal("resource identity must be chart only")
	}
	if len(payload.Resources) != 3 {
		t.Fatal("missing direct resources")
	}
	for kind, v := range payload.Resources {
		u, e := url.Parse(v.URL)
		if e != nil || u.Query().Get("X-Amz-Expires") != "900" || u.Query().Get("X-Amz-Signature") == "" {
			t.Fatal("unsigned or wrong expiry")
		}
		resp, e := http.Get(v.URL)
		if e != nil {
			t.Fatal(e)
		}
		data, e := io.ReadAll(resp.Body)
		resp.Body.Close()
		if e != nil || resp.StatusCode != 200 || int64(len(data)) != v.Size || hash(string(data)) != v.SHA {
			t.Fatal("direct integrity", kind, e)
		}
		if kind == "audio" && !bytes.Equal(data, audio) {
			t.Fatal("direct audio changed")
		}
		resp, e = http.Head(v.HeadURL)
		if e != nil {
			t.Fatal(e)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 || resp.ContentLength != v.Size {
			t.Fatal("direct HEAD", kind)
		}
	}
}
func TestS3PendingRecoveryAndGameIsolation(t *testing.T) {
	pool := scoreTestDB(t)
	ctx := context.Background()
	store := teststore.New(t)
	app := testServer(t, pool, Config{Objects: store, Storage: t.TempDir(), GameOnly: true})
	for _, key := range []string{"abandoned", "recent", "referenced"} {
		teststore.Seed(t, store, key, []byte("test"))
	}
	_, e := pool.Exec(ctx, `INSERT INTO pending_objects(storage_key,created_at) VALUES('abandoned',now()-interval '2 days'),('recent',now()),('referenced',now()-interval '2 days'); INSERT INTO retired_files(storage_key) VALUES('referenced'); BEGIN; INSERT INTO users(id) VALUES('u');
 INSERT INTO charts(id,owner_id,title,bpm,duration,encoding,wave_filename) VALUES('c','u','Recovery',120,10,'utf-8','test.ogg');
 INSERT INTO chart_resources(chart_id,kind,storage_key,original_filename,sha256,byte_size,media_type) SELECT 'c',kind,'referenced','test',repeat('a',64),4,'test' FROM unnest(ARRAY['tja','audio']) kind; COMMIT;`)
	if e != nil {
		t.Fatal(e)
	}
	if e = app.reconcilePending(ctx); e != nil {
		t.Fatal(e)
	}
	if e = app.deleteRetiredFiles(ctx); e != nil {
		t.Fatal(e)
	}
	if f, e := store.Open(ctx, "abandoned"); e == nil {
		f.Close()
		t.Fatal("orphan retained")
	}
	for _, key := range []string{"recent", "referenced"} {
		f, e := store.Open(ctx, key)
		if e != nil {
			t.Fatal("live/pending deleted", e)
		}
		f.Close()
	}
	for _, target := range []struct{ method, path string }{{"GET", "/api/v1/auth/sso/login"}, {"GET", "/api/v1/auth/sso/callback"}, {"POST", "/api/v1/charts"}, {"POST", "/api/v1/scores"}, {"GET", "/api/v1/me"}} {
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, httptest.NewRequest(target.method, target.path, nil))
		if w.Code != 404 {
			t.Fatal("browser endpoint exposed", target, w.Code)
		}
	}
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/game/bootstrap", nil))
	if w.Code != 200 {
		t.Fatal("game inaccessible", w.Code)
	}
}
