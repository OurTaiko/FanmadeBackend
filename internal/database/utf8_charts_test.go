package database

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestUTF8ChartsMigration(t *testing.T) {
	url := os.Getenv("DATABASE_TEST_URL")
	if url == "" {
		t.Skip("DATABASE_TEST_URL required")
	}
	ctx := context.Background()
	admin, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("utf8_migration_%d", time.Now().UnixNano())
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
	if err = migrateTo(ctx, pool, storage, 34); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `BEGIN;
 INSERT INTO users(id,username,password_hash) VALUES('owner','tester','unused');
 INSERT INTO charts(id,owner_id,title,bpm,duration,encoding,wave_filename,difficulties)
 VALUES('chart','owner','Song',120,10,'shift-jis','a.ogg','[{"course":"Oni","level":5,"maker":"","branching":false}]');
 INSERT INTO chart_resources SELECT 'chart',kind,kind,'file',repeat('a',64),10,'test' FROM unnest(ARRAY['tja','audio','archive']) kind;
 INSERT INTO scores(id,user_id,song_id,difficulty,good,ok,bad,score,drumroll,max_combo,payload_digest,idempotency_key)
 VALUES('score','owner','chart','Oni',10,0,0,100,0,10,repeat('a',64),'score-receipt');
 INSERT INTO upload_requests(user_id,idempotency_key,payload_digest,chart_id,tja_sha256,audio_sha256)
 VALUES('owner','upload-receipt',repeat('a',64),'chart',repeat('a',64),repeat('a',64));
 COMMIT;`)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		t.Helper()
		var value string
		err := pool.QueryRow(ctx, `SELECT jsonb_build_array(
   (SELECT jsonb_agg(to_jsonb(c)-'encoding' ORDER BY id) FROM charts c),
   (SELECT jsonb_agg(to_jsonb(r) ORDER BY chart_id,kind) FROM chart_resources r),
   (SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM scores s),
   (SELECT jsonb_agg(to_jsonb(u) ORDER BY user_id,idempotency_key) FROM upload_requests u))::text`).Scan(&value)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := snapshot()
	if err = Migrate(ctx, pool, storage); err == nil || !strings.Contains(err.Error(), "non-UTF-8 charts remain") {
		t.Fatal("legacy encoding must abort migration", err)
	}
	var encoding string
	if err = pool.QueryRow(ctx, `SELECT encoding FROM charts WHERE id='chart'`).Scan(&encoding); err != nil || encoding != "shift-jis" {
		t.Fatal("failed migration discarded encoding", encoding, err)
	}
	var applied bool
	if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=35)`).Scan(&applied); err != nil || applied {
		t.Fatal("failed migration marked applied", applied, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE charts SET encoding='utf-8'`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = Migrate(ctx, pool, storage); err != nil {
			t.Fatal(err)
		}
	}
	var columns int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='charts' AND column_name='encoding'`).Scan(&columns); err != nil || columns != 0 {
		t.Fatal("encoding column remains", columns, err)
	}
	if snapshot() != before {
		t.Fatal("migration changed chart, resource, score or upload data")
	}
}
