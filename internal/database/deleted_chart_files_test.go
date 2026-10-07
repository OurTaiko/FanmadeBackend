package database

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDeletedChartsMigration(t *testing.T) {
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
	name := fmt.Sprintf("deleted_files_migration_%d", time.Now().UnixNano())
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
	if err = migrateTo(ctx, pool, t.TempDir(), 31); err != nil {
		t.Fatal(err)
	}
	// The deleted song's audio is also the live song's audio.
	_, err = pool.Exec(ctx, `BEGIN;
 INSERT INTO users(id,username,password_hash) VALUES('owner','tester','unused');
 INSERT INTO charts(id,owner_id,title,bpm,duration,encoding,wave_filename,status,difficulties) VALUES
  ('live','owner','Live',120,100,'utf-8','audio.ogg','published','[{"course":"Oni","level":5,"maker":""}]'),
  ('gone','owner','Gone',120,100,'utf-8','audio.ogg','deleted','[{"course":"Oni","level":5,"maker":""}]');
 INSERT INTO chart_resources(chart_id,kind,storage_key,original_filename,sha256,byte_size,media_type) VALUES
  ('live','tja','live/tja','f',repeat('a',64),1,'test'),('live','audio','shared','f',repeat('a',64),1,'test'),
  ('gone','tja','gone/tja','f',repeat('a',64),1,'test'),('gone','audio','shared','f',repeat('a',64),1,'test'),
  ('gone','archive','gone/zip','f',repeat('a',64),1,'test');
 INSERT INTO upload_requests(user_id,idempotency_key,payload_digest,chart_id,tja_sha256,audio_sha256) VALUES
  ('owner','gone-upload-receipt',repeat('a',64),'gone',repeat('a',64),repeat('a',64));
 INSERT INTO scores(id,user_id,song_id,difficulty,good,ok,bad,score,drumroll,max_combo,payload_digest) VALUES
  ('s1','owner','live','Oni',1,0,0,1000,0,1,repeat('a',64)),('s2','owner','gone','Oni',1,0,0,1000,0,1,repeat('a',64));
 COMMIT;`)
	if err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, pool, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	var ok bool
	err = pool.QueryRow(ctx, `SELECT
	 (SELECT array_agg(song_id) FROM scores)=ARRAY['live'] AND
	 (SELECT count(*) FROM chart_resources WHERE chart_id='gone')=0 AND
	 (SELECT count(*) FROM chart_resources WHERE chart_id='live')=2 AND
	 (SELECT array_agg(id) FROM charts)=ARRAY['live'] AND
	 (SELECT count(*) FROM upload_requests WHERE chart_id IS NULL)=1 AND
	 (SELECT array_agg(storage_key ORDER BY storage_key) FROM retired_files)=ARRAY['gone/tja','gone/zip','shared']`).Scan(&ok)
	if err != nil || !ok {
		t.Fatal("purge", ok, err)
	}
	// The live song still needs both files; only deleted songs are exempt.
	if _, err = pool.Exec(ctx, `DELETE FROM chart_resources WHERE chart_id='live' AND kind='tja'`); err == nil {
		t.Fatal("live song lost its TJA")
	}
}
