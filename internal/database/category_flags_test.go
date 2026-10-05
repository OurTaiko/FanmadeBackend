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

func TestCategoryFlagsMigration(t *testing.T) {
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
	name := fmt.Sprintf("category_flags_test_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+name); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+name+", "+name+"_auth, "+name+"_internal CASCADE")
	cfg, _ := pgxpool.ParseConfig(url)
	cfg.ConnConfig.RuntimeParams["search_path"] = SchemaSearchPath(name)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	storage := t.TempDir()
	if err = migrateTo(ctx, pool, storage, 28); err != nil {
		t.Fatal(err)
	}
	if err = MigrateSSO(ctx, pool, nil); err != nil {
		t.Fatal(err)
	}
	// All 64 combinations, including an empty legacy membership and hidden and
	// deleted charts, must round-trip without changing resources or receipts.
	_, err = pool.Exec(ctx, `BEGIN;
 INSERT INTO users(id) VALUES('u');
 INSERT INTO charts(id,owner_id,title,bpm,duration,encoding,wave_filename,status,difficulties)
 SELECT n::text,'u','Song '||n,120,10,'utf-8','a.ogg',
  CASE n%3 WHEN 0 THEN 'published' WHEN 1 THEN 'hidden' ELSE 'deleted' END,
  '[{"course":"Oni","level":5,"maker":"A"}]'::jsonb FROM generate_series(0,63) n;
 INSERT INTO chart_resources SELECT id,kind,id||kind,'file',repeat('a',64),1,'test' FROM charts CROSS JOIN unnest(ARRAY['tja','audio']) kind;
 INSERT INTO chart_categories(chart_id,category_id)
 SELECT c.id,m.id FROM charts c CROSS JOIN (VALUES
  ('game',1),('virtual-singer',2),('pop',4),('classic',8),('variety',16),('anime',32)
 ) m(id,flag) WHERE (c.id::integer & m.flag)<>0;
 INSERT INTO scores(id,user_id,song_id,difficulty,good,ok,bad,score,drumroll,max_combo,payload_digest,idempotency_key)
 VALUES('score','u','1','Oni',10,0,0,100,0,10,repeat('a',64),'old-score-receipt');
 INSERT INTO upload_requests(user_id,idempotency_key,payload_digest,chart_id,tja_sha256,audio_sha256)
 VALUES('u','old-upload-receipt',repeat('a',64),'1',repeat('a',64),repeat('a',64));
 COMMIT;
 ALTER TABLE charts DROP CONSTRAINT charts_difficulties_check;
 UPDATE charts SET difficulties='[{"course":"Tower","level":5,"maker":"Legacy"}]',status='hidden' WHERE id='63';
 SET CONSTRAINTS ALL IMMEDIATE;
 ALTER TABLE charts ADD CONSTRAINT charts_difficulties_check CHECK(valid_chart_difficulties(difficulties,is_single)) NOT VALID;`)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		t.Helper()
		var value string
		err := pool.QueryRow(ctx, `SELECT jsonb_build_array(
 (SELECT jsonb_agg(to_jsonb(c)-'category_flags'-'demo_end' ORDER BY id) FROM charts c),
 (SELECT jsonb_agg(to_jsonb(r) ORDER BY chart_id,kind) FROM chart_resources r),
 (SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM scores s),
 (SELECT jsonb_agg(to_jsonb(u) ORDER BY user_id,idempotency_key) FROM upload_requests u))::text`).Scan(&value)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := snapshot()
	if _, err = pool.Exec(ctx, `INSERT INTO categories VALUES('custom','Custom','CUSTOM',7)`); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, pool, storage); err == nil || !strings.Contains(err.Error(), "unknown category") {
		t.Fatal("unknown category must abort migration", err)
	}
	var rolledBack bool
	err = pool.QueryRow(ctx, `SELECT
 NOT EXISTS(SELECT 1 FROM schema_migrations WHERE version=29)
 AND NOT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='charts' AND column_name='category_flags')
 AND (SELECT count(*) FROM chart_categories)=192`).Scan(&rolledBack)
	if err != nil || !rolledBack || snapshot() != before {
		t.Fatal("failed migration changed existing data", err)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM categories WHERE id='custom'`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = MigrateS3(ctx, pool, storage); err != nil {
			t.Fatal(err)
		}
	}
	var good bool
	err = pool.QueryRow(ctx, `SELECT count(*)=64 AND bool_and(category_flags=id::integer) FROM charts`).Scan(&good)
	if err != nil || !good || snapshot() != before {
		t.Fatal("classification or unrelated data changed", err)
	}
	err = pool.QueryRow(ctx, `SELECT count(*)=0 FROM information_schema.tables
 WHERE table_schema=current_schema() AND table_name IN ('categories','chart_categories')`).Scan(&good)
	if err != nil || !good {
		t.Fatal("obsolete tables remain", err)
	}
	for _, value := range []string{"-1", "64", "65", "NULL"} {
		if _, err = pool.Exec(ctx, `UPDATE charts SET category_flags=`+value+` WHERE id='1'`); err == nil {
			t.Fatal("invalid category flags accepted", value)
		}
	}
	if _, err = pool.Exec(ctx, `UPDATE charts SET difficulties='[{"course":"Tower","level":5,"maker":"New"}]' WHERE id='1'`); err == nil {
		t.Fatal("difficulty validation was not restored")
	}
}
