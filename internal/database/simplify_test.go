package database

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSimplifyPreservesDataAndGroupsSchemas(t *testing.T) {
	url := os.Getenv("DATABASE_TEST_URL")
	if url == "" {
		t.Skip("set DATABASE_TEST_URL for migration integration tests")
	}
	ctx := context.Background()
	admin, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("simplify_test_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+name); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+name+", "+name+"_auth, "+name+"_internal CASCADE")
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = SchemaSearchPath(name)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	storage := t.TempDir()
	if err = migrateTo(ctx, pool, storage, 24); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`BEGIN;
 INSERT INTO users(id,username,password_hash) VALUES('u','fixture','unused');
 INSERT INTO files(id,storage_key,original_filename,sha256,byte_size,media_type) VALUES('t','original.tja','original.tja',repeat('a',64),12,'application/octet-stream'),('a','original.ogg','original.ogg',repeat('b',64),20,'audio/ogg');
 INSERT INTO charts(id,owner_id,description,title_override,title_translation_overrides) VALUES('c','u','description','Display title','{"zh":"显示标题"}');
 INSERT INTO chart_data(chart_id,title,subtitle,bpm,duration,encoding,wave_filename,tja_file_id,audio_file_id,validation_version,title_translations) VALUES('c','Original','Subtitle',120,10,'utf-8','original.ogg','t','a','obsolete','{"ja":"曲"}');
 INSERT INTO difficulties(chart_id,block_index,course,level,style,maker) VALUES('c',0,'Oni',8,'Single','Maker');
 INSERT INTO scores(id,user_id,song_id,block_index,difficulty,good,ok,bad,score,drumroll,max_combo,clear_status,idempotency_key,payload_digest,replay_data) VALUES('s','u','c',0,'Oni',100,2,1,900000,3,90,1,'receipt',repeat('a',64),'{"version":1,"inputs":[]}');
 INSERT INTO chart_archives(chart_id,storage_key,sha256,byte_size) VALUES('c','original.zip',repeat('c',64),32);
 INSERT INTO upload_requests(user_id,idempotency_key,payload_digest,chart_id,tja_sha256,audio_sha256) VALUES('u','upload',repeat('a',64),'c',repeat('a',64),repeat('b',64));
 INSERT INTO chart_categories(chart_id,category_id) VALUES('c','variety');
 COMMIT;`)
	cover := []byte("RIFFtestWEBPoriginal-cover")
	digest := fmt.Sprintf("%x", sha256.Sum256(cover))
	exec(`INSERT INTO chart_covers(id,chart_id,webp,sha256) VALUES('cover','c',$1,$2)`, cover, digest)
	// Capture all unaffected business records, including scores and idempotency keys.
	snapshot := func() string {
		t.Helper()
		var result string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(s)) FROM scores s),(SELECT jsonb_agg(to_jsonb(d)) FROM difficulties d),(SELECT jsonb_agg(to_jsonb(r)) FROM upload_requests r),(SELECT jsonb_agg(to_jsonb(c)) FROM chart_categories c))::text`).Scan(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := snapshot()
	if err = MigrateS3(ctx, pool, storage); err == nil || !strings.Contains(err.Error(), "must be migrated to S3") {
		t.Fatalf("S3 accepted local-only cover: %v", err)
	}
	// A mismatched existing export must abort, leaving old tables and bytes intact.
	key := "covers/imported/" + digest + ".webp"
	if err = os.MkdirAll(filepath.Dir(filepath.Join(storage, key)), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(storage, key), []byte("wrong bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, pool, storage); err == nil || !strings.Contains(err.Error(), "integrity mismatch") {
		t.Fatalf("bad cover accepted: %v", err)
	}
	var old bool
	if err = pool.QueryRow(ctx, `SELECT to_regclass('chart_data') IS NOT NULL AND NOT EXISTS(SELECT 1 FROM schema_migrations WHERE version=25)`).Scan(&old); err != nil || !old {
		t.Fatal("migration partially committed", err)
	}
	if err = os.Remove(filepath.Join(storage, key)); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = Migrate(ctx, pool, storage); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(storage, key))
	if err != nil || string(data) != string(cover) {
		t.Fatal("cover lost", err)
	}
	if snapshot() != before {
		t.Fatal("business records changed")
	}
	for _, q := range []string{
		`SELECT count(*) FROM chart_resources WHERE chart_id='c'`,
		`SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name IN ('chart_data','files','chart_covers','chart_archives')`,
		`SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND column_name='validation_version'`,
	} {
		var n int
		if err = pool.QueryRow(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		expected := 0
		if strings.Contains(q, "FROM chart_resources") {
			expected = 4
		}
		if n != expected {
			t.Fatalf("unexpected count %d: %s", n, q)
		}
	}
	var title, display, translation string
	if err = pool.QueryRow(ctx, `SELECT title,title_override,title_translations->>'ja' FROM charts WHERE id='c'`).Scan(&title, &display, &translation); err != nil || title != "Original" || display != "Display title" || translation != "曲" {
		t.Fatal("metadata changed", err)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM chart_resources WHERE chart_id='c' AND kind='tja'`); err == nil {
		t.Fatal("missing TJA accepted")
	}
	if _, err = pool.Exec(ctx, `INSERT INTO charts(id,owner_id,title,bpm,duration,encoding,wave_filename) VALUES('incomplete','u','Missing',120,10,'utf-8','a.ogg')`); err == nil {
		t.Fatal("song without resources accepted")
	}
	if _, err = pool.Exec(ctx, `UPDATE scores SET block_index=42 WHERE id='s'`); err == nil {
		t.Fatal("score FK lost")
	}
	for range 2 {
		if err = MigrateSSO(ctx, pool, func(context.Context, []string) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if err = Migrate(ctx, pool, storage); err != nil {
			t.Fatal(err)
		}
	}
	if snapshot() != before {
		t.Fatal("schema grouping changed records")
	}
	for ns, want := range map[string]int{name: 7, name + "_auth": 2, name + "_internal": 5} {
		var n int
		if err = pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema=$1 AND table_type='BASE TABLE'`, ns).Scan(&n); err != nil || n != want {
			t.Fatalf("schema %s has %d tables, want %d: %v", ns, n, want, err)
		}
	}
	// Triggers still find the queue after it moves to internal, even on a new connection.
	pool.Reset()
	exec(`UPDATE chart_resources SET storage_key='replacement.webp' WHERE chart_id='c' AND kind='cover'`)
	var retired string
	if err = pool.QueryRow(ctx, `SELECT storage_key FROM retired_files`).Scan(&retired); err != nil || retired != key {
		t.Fatal("retirement after schema move failed", err)
	}
	exec(`DELETE FROM chart_resources WHERE chart_id='c' AND kind='archive'`)
}
