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

func TestRemoveChartHistoryMigration(t *testing.T) {
	url := os.Getenv("DATABASE_TEST_URL")
	if url == "" {
		t.Skip("set DATABASE_TEST_URL for PostgreSQL migration tests")
	}
	ctx := context.Background()
	admin, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("current_chart_test_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+name); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+name+" CASCADE")
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	storage := t.TempDir()
	if err = migrateTo(ctx, pool, storage, 23); err != nil {
		t.Fatal(err)
	}
	exec := func(query string) {
		t.Helper()
		if _, err := pool.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	exec(`BEGIN;
INSERT INTO users(id,username,password_hash) VALUES('u','tester','unused');
INSERT INTO files(id,storage_key,original_filename,sha256,byte_size,media_type) VALUES
 ('t','current.tja','chart.tja',repeat('a',64),12,'application/octet-stream'),
 ('a','shared.ogg','music.ogg',repeat('b',64),20,'audio/ogg'),
 ('old','old.tja','chart.tja',repeat('c',64),10,'application/octet-stream');
INSERT INTO charts(id,owner_id,current_version_id) VALUES('c','u','v');
INSERT INTO chart_versions(id,chart_id,version_number,title,bpm,duration,encoding,wave_filename,tja_file_id,audio_file_id,validation_version,title_translations) VALUES
 ('v','c',2,'Current',120,10,'utf-8','music.ogg','t','a','test','{"ja":"曲"}'),
 ('old','c',1,'Old',120,10,'utf-8','music.ogg','old','a','test','{}');
INSERT INTO difficulties(version_id,block_index,course,level,player,style,maker) VALUES('v',0,'Oni',5,'','Single','maker'),('old',0,'Oni',5,'','Single','old');
INSERT INTO chart_archives(version_id,storage_key,sha256,byte_size) VALUES('v','current.zip',repeat('d',64),30),('old','old.zip',repeat('e',64),28);
INSERT INTO chart_covers(id,chart_id,sha256,storage_key,byte_size) VALUES('cover','c',repeat('f',64),'cover.webp',12);
INSERT INTO upload_requests(user_id,idempotency_key,payload_digest,chart_id,version_id) VALUES('u','current-receipt',repeat('1',64),'c','v'),('u','old-receipt',repeat('2',64),'c','old');
INSERT INTO scores(id,user_id,song_id,version_id,block_index,difficulty,good,ok,bad,score,drumroll,max_combo,clear_status,idempotency_key,payload_digest,replay_data) VALUES
 ('score','u','c','v',0,'Oni',100,2,1,900000,3,90,1,'score-receipt',repeat('3',64),'{"version":1,"inputs":[]}'),
 ('historical','u','c','old',0,'Oni',1,0,0,1000,0,1,0,NULL,repeat('4',64),NULL);
ALTER TABLE difficulties DROP CONSTRAINT difficulties_course_check;
INSERT INTO difficulties(version_id,block_index,course,level,player,style,maker) VALUES('v',1,'Tower',5,'','Single','archived');
ALTER TABLE difficulties ADD CONSTRAINT difficulties_course_check CHECK(course IN ('Easy','Normal','Hard','Oni','Edit')) NOT VALID;
UPDATE charts SET status='hidden' WHERE id='c';
COMMIT;`)
	// A migration may not silently attach old-content scores to current files.
	if err = Migrate(ctx, pool, storage); err == nil || !strings.Contains(err.Error(), "historical score") {
		t.Fatalf("unsafe migration: %v", err)
	}
	var partial bool
	if err = pool.QueryRow(ctx, `SELECT to_regclass('chart_data') IS NOT NULL OR EXISTS(SELECT 1 FROM schema_migrations WHERE version=24)`).Scan(&partial); err != nil || partial {
		t.Fatalf("partial migration: %v", err)
	}
	exec(`DELETE FROM scores WHERE id='historical'`)
	var before, after string
	if err = pool.QueryRow(ctx, `SELECT (to_jsonb(s)-'version_id')::text FROM scores s WHERE id='score'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = Migrate(ctx, pool, storage); err != nil {
			t.Fatal(err)
		}
	}
	if err = pool.QueryRow(ctx, `SELECT to_jsonb(s)::text FROM scores s WHERE id='score'`).Scan(&after); err != nil || before != after {
		t.Fatalf("score changed: %v", err)
	}
	for _, query := range []string{
		`SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND column_name IN ('version_id','current_version_id','version_number')`,
		`SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='chart_versions'`,
		`SELECT count(*) FROM files WHERE id='old'`,
	} {
		var n int
		if err = pool.QueryRow(ctx, query).Scan(&n); err != nil || n != 0 {
			t.Fatalf("legacy data retained: %d %v", n, err)
		}
	}
	for _, query := range []string{
		`SELECT count(*) FROM chart_data WHERE chart_id='c' AND title='Current' AND title_translations->>'ja'='曲'`,
		`SELECT count(*) FROM difficulties WHERE chart_id='c' AND maker='maker'`,
		`SELECT count(*) FROM chart_archives WHERE chart_id='c' AND storage_key='current.zip'`,
		`SELECT count(*) FROM chart_covers WHERE chart_id='c' AND storage_key='cover.webp'`,
		`SELECT count(*) FROM files WHERE id='a'`,
		`SELECT count(*) FROM difficulties WHERE chart_id='c' AND course='Tower'`,
		`SELECT count(*) FROM retired_files WHERE storage_key='old.tja'`,
		`SELECT count(*) FROM retired_files WHERE storage_key='old.zip'`,
		`SELECT count(*) FROM upload_requests WHERE idempotency_key='current-receipt' AND tja_sha256=repeat('a',64) AND audio_sha256=repeat('b',64)`,
		`SELECT count(*) FROM upload_requests WHERE idempotency_key='old-receipt' AND tja_sha256=repeat('c',64)`,
	} {
		var n int
		if err = pool.QueryRow(ctx, query).Scan(&n); err != nil || n != 1 {
			t.Fatalf("current content/cleanup/receipt lost: %d %v (%s)", n, err, query)
		}
	}
	if _, err = pool.Exec(ctx, `UPDATE scores SET block_index=99 WHERE id='score'`); err == nil {
		t.Fatal("score target foreign key missing")
	}
	if _, err = pool.Exec(ctx, `DELETE FROM chart_data WHERE chart_id='c'`); err == nil {
		t.Fatal("song accepted missing current data")
	}
}
