package database

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Uses a disposable schema; never writes to application tables.
func TestCloudScoreMigration(t *testing.T) {
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
	name := fmt.Sprintf("score_policy_test_%d", time.Now().UnixNano())
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
	_, err = pool.Exec(ctx, schema+eseSchema+`
 CREATE TABLE schema_migrations(version integer PRIMARY KEY, applied_at timestamptz DEFAULT now());
 INSERT INTO schema_migrations(version) VALUES(1),(2);
 INSERT INTO users(id,username,password_hash) VALUES('u','tester','unused');
 INSERT INTO files(id,storage_key,original_filename,sha256,byte_size,media_type) VALUES
 ('t','chart.tja','chart.tja',repeat('a',64),1,'application/octet-stream'),
 ('a','music.ogg','music.ogg',repeat('b',64),1,'audio/ogg');`)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO charts(id,owner_id,current_version_id) VALUES('c','u','v');
 INSERT INTO chart_versions(id,chart_id,version_number,title,bpm,duration,encoding,wave_filename,tja_file_id,audio_file_id,validation_version)
 VALUES('v','c',1,'Test',120,10,'utf-8','music.ogg','t','a','tja-upload-v1');
 UPDATE chart_versions SET maker='Legacy Maker' WHERE id='v';
 INSERT INTO difficulties(version_id,block_index,course,level,player)
 VALUES('v',0,'Oni',5,''),('v',1,'Oni',5,''),('v',2,'Oni',5,'P2'),('v',3,'Easy',3,'');`)
	if err != nil {
		tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	storage := t.TempDir()
	// Missing originals must roll back the entire migration, not guess Single.
	if err = Migrate(ctx, pool, storage); err == nil {
		t.Fatal("expected missing file failure")
	}
	var migrated bool
	if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=3)`).Scan(&migrated); err != nil || migrated {
		t.Fatalf("partial migration: %v", err)
	}
	data := "TITLE:Test\nTITLEJA:日本語\nSUBTITLEZH:副标题\nBPM:120\nWAVE:music.ogg\nCOURSE:Oni\nLEVEL:5\n#START\n1000,\n#END\nSTYLE:Double\n#START\n1000,\n#END\n#START P2\n2000,\n#END\n"
	data += "COURSE:Easy\nLEVEL:3\n#START\n1000,\n#END\n"
	if err = os.WriteFile(filepath.Join(storage, "chart.tja"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = Migrate(ctx, pool, storage); err != nil {
			t.Fatal(err)
		}
	}
	var makerCount int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM difficulties WHERE maker='Legacy Maker'`).Scan(&makerCount); err != nil || makerCount != 4 {
		t.Fatalf("014 must preserve every block credit: %d %v", makerCount, err)
	}
	var oldMakerColumn bool
	if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='chart_versions' AND column_name='maker')`).Scan(&oldMakerColumn); err != nil || oldMakerColumn {
		t.Fatalf("014 must remove version maker: %v", err)
	}
	var category string
	if err = pool.QueryRow(ctx, `SELECT category_id FROM chart_categories WHERE chart_id='c'`).Scan(&category); err != nil || category != "variety" {
		t.Fatalf("013 did not backfill existing chart: %s %v", category, err)
	}
	var title, subtitle string
	if err = pool.QueryRow(ctx, `SELECT title_translations->>'ja',subtitle_translations->>'zh' FROM chart_versions WHERE id='v'`).Scan(&title, &subtitle); err != nil || title != "日本語" || subtitle != "副标题" {
		t.Fatalf("localized backfill: %s %s %v", title, subtitle, err)
	}
	rows, err := pool.Query(ctx, `SELECT style,cloud_score_eligible FROM difficulties ORDER BY block_index`)
	if err != nil {
		t.Fatal(err)
	}
	i := 0
	for rows.Next() {
		var style string
		var eligible bool
		if err = rows.Scan(&style, &eligible); err != nil {
			t.Fatal(err)
		}
		expected := "Double"
		if i == 0 || i == 3 {
			expected = "Single"
		}
		if style != expected || eligible != (i == 0 || i == 3) {
			t.Fatalf("block %d: %s %t", i, style, eligible)
		}
		i++
	}
	err = rows.Err()
	rows.Close()
	if err != nil || i != 4 {
		t.Fatalf("rows=%d error=%v", i, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE difficulties SET cloud_score_eligible=true WHERE block_index=1`); err == nil {
		t.Fatal("eligibility must be database-generated")
	}
	if _, err = pool.Exec(ctx, `UPDATE difficulties SET style='Single' WHERE player='P2'`); err == nil {
		t.Fatal("P2 must not become Single")
	}
	// Simulate the v9 score schema and an existing historical play.
	if _, err = pool.Exec(ctx, `ALTER TABLE scores DROP COLUMN max_combo;
	 DELETE FROM schema_migrations WHERE version=10;
	 INSERT INTO scores(id,user_id,song_id,version_id,block_index,difficulty,good,ok,bad,score,drumroll,payload_digest)
	 VALUES('old-score','u','c','v',0,'Oni',10,2,1,9000,5,repeat('c',64));`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = Migrate(ctx, pool, storage); err != nil {
			t.Fatal(err)
		}
	}
	var points, combo int64
	var digest string
	if err = pool.QueryRow(ctx, `SELECT score,max_combo,payload_digest FROM scores WHERE id='old-score'`).Scan(&points, &combo, &digest); err != nil || points != 9000 || combo != 0 || digest != strings.Repeat("c", 64) {
		t.Fatalf("010 did not preserve historical score: %d %d %s %v", points, combo, digest, err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO scores(id,user_id,song_id,version_id,block_index,difficulty,good,ok,bad,score,drumroll,payload_digest)
	 VALUES('missing-combo','u','c','v',0,'Oni',10,2,1,9000,5,repeat('d',64))`); err == nil {
		t.Fatal("database accepted a score without an explicit maximum combo")
	}
	// Re-run 013 with an existing score and compare complete business rows.
	before := map[string]string{}
	for _, table := range []string{"users", "files", "charts", "chart_versions", "difficulties", "scores", "upload_requests"} {
		var rows string
		if err = pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM `+table+` t`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		before[table] = rows
	}
	if _, err = pool.Exec(ctx, `DROP TABLE chart_categories,categories; DELETE FROM schema_migrations WHERE version=13`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = Migrate(ctx, pool, storage); err != nil {
			t.Fatal(err)
		}
	}
	for table, want := range before {
		var rows string
		if err = pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM `+table+` t`).Scan(&rows); err != nil || rows != want {
			t.Fatalf("013 changed %s: %v", table, err)
		}
	}
	// Reproduce a database already migrated by the initial COURSE-leaking parser.
	if _, err = pool.Exec(ctx, `UPDATE difficulties SET style='Double' WHERE block_index=3;
	 DELETE FROM schema_migrations WHERE version=4`); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, pool, storage); err != nil {
		t.Fatal(err)
	}
	var eligible bool
	if err = pool.QueryRow(ctx, `SELECT cloud_score_eligible FROM difficulties WHERE block_index=3`).Scan(&eligible); err != nil || !eligible {
		t.Fatalf("004 did not repair Easy: %v", err)
	}
}
