package database

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTitleTranslationsMigration(t *testing.T) {
	url := os.Getenv("DATABASE_TEST_URL")
	if url == "" {
		t.Skip("set DATABASE_TEST_URL for migration tests")
	}
	ctx := context.Background()
	admin, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("titles_test_%d", time.Now().UnixNano())
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
	if err = migrateTo(ctx, pool, storage, 25); err != nil {
		t.Fatal(err)
	}
	if err = MigrateSSO(ctx, pool, nil); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `BEGIN;
 INSERT INTO users(id) VALUES('u');
 INSERT INTO charts(id,owner_id,title,subtitle,bpm,duration,encoding,wave_filename,title_override,subtitle_override,title_translations,subtitle_translations,title_translation_overrides,subtitle_translation_overrides) VALUES
 ('c','u','Raw title','Raw subtitle',120,10,'utf-8','a.ogg','English edit','','{"ja":"日本語","ko":"한국어","zh":"旧中文"}','{"ja":"副題"}','{"zh":"新中文"}','{"ko":"한국어 부제"}'),
 ('plain','u','Source','Subtitle',120,10,'utf-8','a.ogg',NULL,NULL,'{}','{}','{}','{}');
 INSERT INTO chart_resources(chart_id,kind,storage_key,original_filename,sha256,byte_size,media_type) SELECT id,kind,id||kind,'file',repeat('a',64),1,'test' FROM charts CROSS JOIN unnest(ARRAY['tja','audio']) kind;
 COMMIT;`)
	if err != nil {
		t.Fatal(err)
	}
	var before, after string
	stable := `SELECT jsonb_agg(to_jsonb(c)-'title_override'-'subtitle_override'-'title_translations'-'subtitle_translations'-'title_translation_overrides'-'subtitle_translation_overrides' ORDER BY id)::text FROM charts c`
	if err = pool.QueryRow(ctx, stable).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = Migrate(ctx, pool, storage); err != nil {
			t.Fatal(err)
		}
		if err = MigrateSSO(ctx, pool, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err = pool.QueryRow(ctx, stable).Scan(&after); err != nil || before != after {
		t.Fatal("nontranslation data changed", err)
	}
	var good bool
	if err = pool.QueryRow(ctx, `SELECT title='Raw title' AND subtitle='Raw subtitle' AND title_translations='{"en":"English edit","ja":"日本語","zh":"新中文","ko":"한국어"}'::jsonb AND subtitle_translations='{"en":"","ja":"副題","ko":"한국어 부제"}'::jsonb FROM charts WHERE id='c'`).Scan(&good); err != nil || !good {
		t.Fatal("translations or source lost", err)
	}
	if err = pool.QueryRow(ctx, `SELECT title_translations='{"en":"Source"}'::jsonb AND subtitle_translations='{"en":"Subtitle"}'::jsonb FROM charts WHERE id='plain'`).Scan(&good); err != nil || !good {
		t.Fatal("English backfill lost", err)
	}
	var n int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND column_name LIKE '%override%'`).Scan(&n); err != nil || n != 0 {
		t.Fatal("override fields remain", n, err)
	}
}
