package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ourtaiko.dev/fanmade/api/internal/teststore"
)

func TestDeleteChartRemovesScoresAndUnsharedFiles(t *testing.T) {
	pool := scoreTestDB(t)
	ctx := context.Background()
	const owner, other = "d46774d30dd13b92d9e536808da468a4", "9b893bc6d9422c93536ff0df503b81e9"
	tokens := map[string]string{owner: strings.Repeat("1", 64), other: strings.Repeat("2", 64)}
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,password_hash,is_admin) VALUES($1,'owner','unused',false),($2,'other','unused',false)`, owner, other); err != nil {
		t.Fatal(err)
	}
	for id, token := range tokens {
		if _, err := pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,$2,'csrf',now()+interval '1 day')`, hash(token), id); err != nil {
			t.Fatal(err)
		}
	}
	store := teststore.New(t)
	// "shared" is the deleted song's audio and also the kept song's audio.
	keys := []string{"gone/tja", "shared", "gone/cover", "gone/archive", "gone/preview", "kept/tja"}
	for _, key := range keys {
		teststore.Seed(t, store, key, []byte("test file data"))
	}
	_, err := pool.Exec(ctx, `BEGIN;
 INSERT INTO charts(id,owner_id,title,bpm,duration,encoding,wave_filename,difficulties) VALUES
  ('gone','`+owner+`','Gone',120,10,'utf-8','a.ogg','[{"course":"Oni","level":5,"maker":""}]'),
  ('kept','`+owner+`','Kept',120,10,'utf-8','a.ogg','[{"course":"Oni","level":5,"maker":""}]');
 INSERT INTO chart_resources(chart_id,kind,storage_key,original_filename,sha256,byte_size,media_type) VALUES
  ('gone','tja','gone/tja','a.tja',repeat('a',64),14,'text/plain'),
  ('gone','audio','shared','a.ogg',repeat('a',64),14,'audio/ogg'),
  ('gone','cover','gone/cover','cover.webp',repeat('a',64),14,'image/webp'),
  ('gone','archive','gone/archive','a.zip',repeat('a',64),14,'application/zip'),
  ('gone','preview','gone/preview','preview.ogg',repeat('a',64),14,'audio/ogg'),
  ('kept','tja','kept/tja','a.tja',repeat('a',64),14,'text/plain'),
  ('kept','audio','shared','a.ogg',repeat('a',64),14,'audio/ogg');
 COMMIT;`)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []struct{ id, user, song string }{{"s1", owner, "gone"}, {"s2", other, "gone"}, {"s3", other, "kept"}} {
		if _, err = pool.Exec(ctx, `INSERT INTO scores(id,user_id,song_id,difficulty,good,ok,bad,score,drumroll,max_combo,payload_digest) VALUES($1,$2,$3,'Oni',1,0,0,1000,0,1,repeat('a',64))`, s.id, s.user, s.song); err != nil {
			t.Fatal(err)
		}
	}
	handler := testServer(t, pool, Config{Objects: store, Storage: t.TempDir(), Origin: "http://localhost"}).Handler()
	remove := func(actor string) int {
		r := httptest.NewRequest("DELETE", "/api/v1/charts/gone", nil)
		r.Header.Set("Origin", "http://localhost")
		r.Header.Set("X-CSRF-Token", "csrf")
		r.AddCookie(&http.Cookie{Name: "ourtaiko_session", Value: tokens[actor]})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	exists := func(key string) bool {
		f, err := store.Open(ctx, key)
		if err == nil {
			f.Close()
		}
		return err == nil
	}
	if code := remove(other); code != 403 {
		t.Fatal("non-owner delete", code)
	}
	if count(`SELECT count(*) FROM scores`) != 3 || !exists("gone/tja") {
		t.Fatal("rejected delete changed data")
	}
	if code := remove(owner); code != 200 {
		t.Fatal("owner delete", code)
	}
	if count(`SELECT count(*) FROM scores WHERE song_id='gone'`) != 0 || count(`SELECT count(*) FROM scores WHERE song_id='kept'`) != 1 {
		t.Fatal("scores not limited to the deleted song")
	}
	if count(`SELECT count(*) FROM chart_resources WHERE chart_id='gone'`) != 0 || count(`SELECT count(*) FROM chart_resources WHERE chart_id='kept'`) != 2 {
		t.Fatal("resources not limited to the deleted song")
	}
	if count(`SELECT count(*) FROM charts WHERE id='gone' AND status='deleted'`) != 1 {
		t.Fatal("tombstone missing")
	}
	for _, key := range []string{"gone/tja", "gone/cover", "gone/archive", "gone/preview"} {
		if exists(key) {
			t.Fatal("file retained", key)
		}
	}
	if !exists("shared") || !exists("kept/tja") {
		t.Fatal("deleted a file another song uses")
	}
	if count(`SELECT count(*) FROM retired_files`) != 0 {
		t.Fatal("cleanup queue not drained")
	}
	if code := remove(owner); code != 404 {
		t.Fatal("repeated delete", code)
	}
	// The deleted song may not regain files without becoming a live song again.
	if _, err = pool.Exec(ctx, `UPDATE charts SET status='published' WHERE id='gone'`); err == nil {
		t.Fatal("restored a song without files")
	}
}
