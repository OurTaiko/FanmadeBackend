package database

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"testing"
	"time"
)

func TestChartDifficultiesJSONMigration(t *testing.T) {
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
	name := fmt.Sprintf("mode_test_%d", time.Now().UnixNano())
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
	if err = migrateTo(ctx, pool, t.TempDir(), 27); err != nil {
		t.Fatal(err)
	}
	if err = MigrateSSO(ctx, pool, nil); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `BEGIN;
 INSERT INTO users(id) VALUES('u');
 INSERT INTO charts(id,owner_id,title,bpm,duration,encoding,wave_filename) VALUES('single','u','S',120,10,'utf-8','a.ogg'),('double','u','D',120,10,'utf-8','a.ogg');
 INSERT INTO chart_resources SELECT id,kind,id||kind,'file',repeat('a',64),1,'test' FROM charts CROSS JOIN unnest(ARRAY['tja','audio']) kind;
 INSERT INTO difficulties(chart_id,block_index,course,level,player,style,maker) VALUES('single',0,'Oni',9,'','Single','A'),('double',0,'Oni',10,'P2','Double','B'),('double',1,'Oni',8,'P1','Double','C');
 INSERT INTO scores(id,user_id,song_id,block_index,difficulty,good,ok,bad,score,drumroll,max_combo,payload_digest) VALUES('score','u','single',0,'Oni',10,0,0,100,0,10,repeat('a',64)); COMMIT;`)
	if err != nil {
		t.Fatal(err)
	}
	// Ambiguous legacy rows must abort, without partly removing the table.
	_, err = pool.Exec(ctx, `INSERT INTO difficulties(chart_id,block_index,course,level,player,style) VALUES('single',1,'Hard',5,'P1','Double')`)
	if err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, pool, t.TempDir()); err == nil {
		t.Fatal("mixed legacy chart accepted")
	}
	if _, err = pool.Exec(ctx, `DELETE FROM difficulties WHERE chart_id='single' AND block_index=1`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = Migrate(ctx, pool, t.TempDir()); err != nil {
			t.Fatal(err)
		}
	}
	var good bool
	err = pool.QueryRow(ctx, `SELECT is_single AND difficulties='[{"course":"Oni","level":9,"maker":"A"}]'::jsonb FROM charts WHERE id='single'`).Scan(&good)
	if err != nil || !good {
		t.Fatal("single lost", err)
	}
	err = pool.QueryRow(ctx, `SELECT NOT is_single AND difficulties='[{"course":"Oni_2p","level":10,"maker":"B"},{"course":"Oni_1p","level":8,"maker":"C"}]'::jsonb FROM charts WHERE id='double'`).Scan(&good)
	if err != nil || !good {
		t.Fatal("double/order lost", err)
	}
	err = pool.QueryRow(ctx, `SELECT count(*)=1 AND min(payload_digest)=repeat('a',64) FROM scores`).Scan(&good)
	if err != nil || !good {
		t.Fatal("score/digest lost", err)
	}
	var count int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='difficulties'`).Scan(&count)
	if err != nil || count != 0 {
		t.Fatal("old table remains", err)
	}
	for _, q := range []string{
		`UPDATE charts SET is_single=false WHERE id='single'`,
		`UPDATE charts SET difficulties='[{"course":"Oni","level":9,"maker":"A"},{"course":"Oni","level":8,"maker":"B"}]' WHERE id='single'`,
		`UPDATE charts SET difficulties='[{"course":"Oni","level":1.5,"maker":"A"}]' WHERE id='single'`,
		`UPDATE charts SET difficulties='[{"course":"Oni","level":9}]' WHERE id='single'`,
		`UPDATE charts SET difficulties='[]' WHERE id='single'`,
		`UPDATE scores SET difficulty='Oni_1p' WHERE id='score'`,
	} {
		if _, err = pool.Exec(ctx, q); err == nil {
			t.Fatal("invalid metadata/score accepted", q)
		}
	}
	if _, err = pool.Exec(ctx, `UPDATE scores SET song_id='double',difficulty='Oni_1p' WHERE id='score'`); err != nil {
		t.Fatal("double score rejected", err)
	}
}
