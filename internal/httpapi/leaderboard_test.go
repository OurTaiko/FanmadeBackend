package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLeaderboard(t *testing.T) {
	pool := scoreTestDB(t)
	ctx := context.Background()
	const song = "11111111111111111111111111111111"
	const current = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const old = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for i := 0; i < 23; i++ {
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,password_hash) VALUES($1,$2,'unused')`, fmt.Sprintf("u%d", i), fmt.Sprintf("player%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	_, err := pool.Exec(ctx, `BEGIN;
	 INSERT INTO files(id,storage_key,original_filename,sha256,byte_size,media_type) VALUES
	 ('t','t','a.tja',repeat('a',64),1,'application/octet-stream'),('a','a','a.ogg',repeat('b',64),1,'audio/ogg');
	 INSERT INTO charts(id,owner_id,current_version_id) VALUES
	 ('11111111111111111111111111111111','u0','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'),
	 ('22222222222222222222222222222222','u0','cccccccccccccccccccccccccccccccc');
	 INSERT INTO chart_versions(id,chart_id,version_number,title,bpm,duration,encoding,wave_filename,tja_file_id,audio_file_id,validation_version) VALUES
	 ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','11111111111111111111111111111111',1,'Old',120,10,'utf-8','a.ogg','t','a','test'),
	 ('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','11111111111111111111111111111111',2,'Current',120,10,'utf-8','a.ogg','t','a','test'),
	 ('cccccccccccccccccccccccccccccccc','22222222222222222222222222222222',1,'Other',120,10,'utf-8','a.ogg','t','a','test');
	 INSERT INTO difficulties(version_id,block_index,course,level,player,style) VALUES
	 ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',0,'Oni',5,'','Single'),
	 ('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',0,'Oni',5,'','Single'),
	 ('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',1,'Hard',5,'','Single'),
	 ('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',2,'Edit',5,'P1','Double'),
	 ('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',3,'Edit',5,'P2','Double'),
	 ('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',4,'Easy',5,'','Single'),
	 ('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',5,'Easy',5,'','Single'),
	 ('cccccccccccccccccccccccccccccccc',0,'Oni',5,'','Single');
	 COMMIT;`)
	if err != nil {
		t.Fatal(err)
	}
	add := func(id, user, chart, version string, points, good int64, date string) {
		t.Helper()
		_, err := pool.Exec(ctx, `INSERT INTO scores(id,user_id,song_id,version_id,block_index,difficulty,good,ok,bad,score,drumroll,payload_digest,submitted_at)
		 VALUES($1,$2,$3,$4,0,'Oni',$5,2,1,$6,55,repeat('a',64),$7::timestamptz)`, id, user, chart, version, good, points, date)
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 23; i++ {
		points := int64(800000 - i)
		if i < 2 {
			points = 900000
		}
		add(fmt.Sprintf("score%02d", i), fmt.Sprintf("u%d", i), song, current, points, 300, "2026-01-01T00:00:00Z")
	}
	add("lower", "u0", song, current, 840000, 100, "2025-12-01T00:00:00Z")
	add("later", "u0", song, current, 900000, 999, "2026-02-01T00:00:00Z")
	add("old", "u0", song, old, 1200000, 500, "2025-12-01T00:00:00Z")
	add("other", "u0", "22222222222222222222222222222222", "cccccccccccccccccccccccccccccccc", 1300000, 600, "2025-12-01T00:00:00Z")
	handler := New(pool, Config{}).Handler()
	get := func(path string, status int, code string) leaderboardResponse {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != status || (code != "" && !strings.Contains(w.Body.String(), code)) {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var v leaderboardResponse
		if status == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), "email") {
				t.Fatal("private user fields exposed")
			}
		}
		return v
	}
	path := "/api/v1/charts/" + song + "/leaderboard"
	v := get(path, 200, "")
	if v.Difficulty != "Oni" || v.VersionID != current || !v.Supported || v.Total != 23 || len(v.Items) != 20 {
		t.Fatalf("unexpected summary: %+v", v)
	}
	if v.Items[0].ID != "score00" || v.Items[0].Good != 300 || v.Items[0].Score.Score != 900000 || v.Items[0].Username != "player0" || v.Items[0].Rank != 1 || v.Items[1].Rank != 1 || v.Items[2].Rank != 3 {
		t.Fatalf("best score/ties incorrect: %+v", v.Items[:3])
	}
	second := get(path+"?difficulty=oni&page=2", 200, "")
	if len(second.Items) != 3 || second.Items[0].Rank != 21 || second.Total != 23 {
		t.Fatalf("pagination incorrect: %+v", second)
	}
	if v := get(path+"?page=3", 200, ""); v.Total != 23 || len(v.Items) != 0 {
		t.Fatal(v)
	}
	if v := get(path+"?difficulty=Hard", 200, ""); !v.Supported || v.Total != 0 || v.Items == nil {
		t.Fatal(v)
	}
	if v := get(path+"?difficulty=ura", 200, ""); v.Supported || v.Difficulty != "Edit" || len(v.Items) != 0 {
		t.Fatal(v)
	}
	get(path+"?difficulty=Easy", 409, "DIFFICULTY_AMBIGUOUS")
	get(path+"?difficulty=Normal", 404, "DIFFICULTY_NOT_FOUND")
	get(path+"?difficulty=invalid", 400, "DIFFICULTY_INVALID")
	for _, page := range []string{"0", "-1", "10001", "invalid", "1.5"} {
		get(path+"?page="+page, 400, "QUERY_INVALID")
	}
	get(path+"?versionId="+old, 409, "CHART_VERSION_CHANGED")
	get("/api/v1/charts/missing/leaderboard", 404, "CHART_NOT_FOUND")
	if _, err := pool.Exec(ctx, `UPDATE charts SET title_override='Renamed' WHERE id=$1`, song); err != nil {
		t.Fatal(err)
	}
	if v := get(path, 200, ""); v.Items[0].ID != "score00" || v.Total != 23 {
		t.Fatal("metadata edit changed leaderboard")
	}
	if _, err := pool.Exec(ctx, `UPDATE charts SET current_version_id=$1 WHERE id=$2`, old, song); err != nil {
		t.Fatal(err)
	}
	if v := get(path, 200, ""); v.Total != 1 || v.Items[0].ID != "old" {
		t.Fatal("versions mixed")
	}
	if _, err := pool.Exec(ctx, `UPDATE charts SET status='deleted' WHERE id=$1`, song); err != nil {
		t.Fatal(err)
	}
	get(path, 404, "CHART_NOT_FOUND")
}
