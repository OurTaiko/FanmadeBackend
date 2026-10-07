package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"ourtaiko.dev/fanmade/api/internal/teststore"
)

func TestBranchingBackfill(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(fmt.Sprint("s3=", remote), func(t *testing.T) {
			ctx := context.Background()
			pool := scoreTestDB(t)
			const owner = "d46774d30dd13b92d9e536808da468a4"
			if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,password_hash) VALUES($1,'author','unused')`, owner); err != nil {
				t.Fatal(err)
			}
			cfg := Config{Storage: t.TempDir(), Origin: "http://localhost"}
			if remote {
				cfg.Objects = teststore.New(t)
			}
			app := testServer(t, pool, cfg)
			put := func(key, text string) {
				if err := app.Config.Objects.Put(ctx, key, strings.NewReader(text), int64(len(text)), "application/octet-stream", hash(text)); err != nil {
					t.Fatal(err)
				}
			}
			put("single.tja", "TITLE:Single\nBPM:120\nWAVE:audio.ogg\nCOURSE:Hard\nLEVEL:6\n#START\n1000,\n#END\n"+
				"COURSE:Oni\nLEVEL:9\n#START\n#BRANCHSTART p,80,95\n#N\n1,\n#E\n2,\n#M\n3,\n#BRANCHEND\n#END\n")
			put("double.tja", "TITLE:Double\nBPM:120\nWAVE:audio.ogg\nCOURSE:Oni\nLEVEL:9\nSTYLE:Double\n#START P1\n1,\n#END\n#START P2\n#BRANCHSTART r,1,2\n#N\n1,\n#BRANCHEND\n#END\n")
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			put("broken.tja", "TITLE:Broken\nBPM:120\nWAVE:audio.ogg\nCOURSE:Easy\nLEVEL:1\n#START\n1,\n#END\n")
			for _, c := range []struct{ id, single, difficulties, tja string }{
				{"single", "true", `[{"course":"Hard","level":6,"maker":"A"},{"course":"Oni","level":9,"maker":"B"}]`, "single.tja"},
				{"double", "false", `[{"course":"Oni_1p","level":9,"maker":""},{"course":"Oni_2p","level":9,"maker":""}]`, "double.tja"},
				// The stored file no longer matches the row: reported and retried, never guessed.
				{"broken", "true", `[{"course":"Oni","level":3,"maker":""}]`, "broken.tja"},
			} {
				if _, err := tx.Exec(ctx, `INSERT INTO charts(id,owner_id,title,bpm,duration,encoding,wave_filename,demo_start,is_single,difficulties) VALUES($1,$2,$1,120,1,'utf-8','audio.ogg',0,$3::boolean,$4::jsonb)`, c.id, owner, c.single, c.difficulties); err != nil {
					t.Fatal(c.id, err)
				}
				if _, err := tx.Exec(ctx, `INSERT INTO chart_resources(chart_id,kind,storage_key,original_filename,sha256,byte_size,media_type) VALUES($1,'audio','audio','audio.ogg',repeat('b',64),1,'audio/ogg'),($1,'tja',$2,'song.tja',repeat('a',64),1,'application/octet-stream')`, c.id, c.tja); err != nil {
					t.Fatal(c.id, err)
				}
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			// Archived unsupported metadata is left alone instead of failing the validator.
			if _, err = pool.Exec(ctx, `BEGIN; ALTER TABLE charts DROP CONSTRAINT charts_difficulties_check;
 INSERT INTO charts(id,owner_id,title,bpm,duration,encoding,wave_filename,demo_start,difficulties) VALUES('archived','`+owner+`','archived',120,1,'utf-8','audio.ogg',0,'[{"course":"Tower","level":3,"maker":""}]');
 INSERT INTO chart_resources(chart_id,kind,storage_key,original_filename,sha256,byte_size,media_type) VALUES('archived','audio','audio','audio.ogg',repeat('b',64),1,'audio/ogg'),('archived','tja','broken.tja','song.tja',repeat('a',64),1,'application/octet-stream');
 SET CONSTRAINTS ALL IMMEDIATE;
 ALTER TABLE charts ADD CONSTRAINT charts_difficulties_check CHECK(valid_chart_difficulties(difficulties,is_single)) NOT VALID; COMMIT;`); err != nil {
				t.Fatal(err)
			}
			if err := app.BackfillBranching(ctx); err == nil || !strings.Contains(err.Error(), "no Oni block") {
				t.Fatal("the mismatched chart must be reported:", err)
			}
			read := func(id string) string {
				var data string
				if err := pool.QueryRow(ctx, `SELECT difficulties::text FROM charts WHERE id=$1`, id).Scan(&data); err != nil {
					t.Fatal(err)
				}
				return data
			}
			want := map[string]string{
				"single":   `[{"level": 6, "maker": "A", "course": "Hard", "branching": false}, {"level": 9, "maker": "B", "course": "Oni", "branching": true}]`,
				"double":   `[{"level": 9, "maker": "", "course": "Oni_1p", "branching": false}, {"level": 9, "maker": "", "course": "Oni_2p", "branching": true}]`,
				"broken":   `[{"level": 3, "maker": "", "course": "Oni"}]`,
				"archived": `[{"level": 3, "maker": "", "course": "Tower"}]`,
			}
			for id, data := range want {
				if got := read(id); got != data {
					t.Fatalf("%s: %s", id, got)
				}
			}

			// Published charts expose the flag to the game and the website.
			w := httptest.NewRecorder()
			app.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/charts/single", nil))
			var body struct {
				Difficulties []map[string]any `json:"difficulties"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Difficulties) != 2 ||
				body.Difficulties[0]["branching"] != false || body.Difficulties[1]["branching"] != true {
				t.Fatal(w.Code, w.Body.String())
			}

			// Once fixed, the next pass completes it; finished charts are not touched again.
			if _, err := pool.Exec(ctx, `UPDATE charts SET difficulties='[{"course":"Easy","level":1,"maker":""}]' WHERE id='broken'`); err != nil {
				t.Fatal(err)
			}
			if err := app.BackfillBranching(ctx); err != nil {
				t.Fatal(err)
			}
			if got := read("broken"); got != `[{"level": 1, "maker": "", "course": "Easy", "branching": false}]` {
				t.Fatal(got)
			}
		})
	}
}
