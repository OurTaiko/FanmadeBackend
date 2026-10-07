package database

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDemoEndMigration(t *testing.T) {
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
	name := fmt.Sprintf("preview_migration_%d", time.Now().UnixNano())
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
	if err = migrateTo(ctx, pool, t.TempDir(), 29); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `BEGIN;
 INSERT INTO users(id,username,password_hash) VALUES('owner','tester','unused');
 INSERT INTO charts(id,owner_id,title,bpm,duration,encoding,wave_filename,demo_start,status)
 VALUES('one','owner','One',120,100,'utf-8','audio.ogg',12.5,'published'),('two','owner','Two',120,100,'utf-8','audio.ogg',98,'deleted');
 INSERT INTO chart_resources SELECT id,kind,id||kind,'file',repeat('a',64),1,'test' FROM charts CROSS JOIN unnest(ARRAY['tja','audio']) kind;
 COMMIT;`)
	if err != nil {
		t.Fatal(err)
	}
	// Stop before 033, which removes the deleted chart.
	if err = migrateTo(ctx, pool, t.TempDir(), 32); err != nil {
		t.Fatal(err)
	}
	var correct bool
	if err = pool.QueryRow(ctx, `SELECT count(*)=2 AND bool_and(demo_end=demo_start+15) FROM charts`).Scan(&correct); err != nil || !correct {
		t.Fatal("backfill", correct, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE charts SET demo_end=20 WHERE id='one'`); err != nil {
		t.Fatal(err)
	}
	if err = migrateTo(ctx, pool, t.TempDir(), 32); err != nil {
		t.Fatal(err)
	}
	var end float64
	if err = pool.QueryRow(ctx, `SELECT demo_end FROM charts WHERE id='one'`).Scan(&end); err != nil || end != 20 {
		t.Fatal("rerun overwrote edit", end, err)
	}
}
