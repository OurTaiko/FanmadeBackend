package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSupportedCoursesAPI(t *testing.T) {
	pool := scoreTestDB(t)
	ctx := context.Background()
	// Recreate the previous schema to exercise migration of real legacy records.
	_, err := pool.Exec(ctx, `ALTER TABLE charts DROP CONSTRAINT charts_difficulties_check;

 ALTER TABLE scores DROP CONSTRAINT scores_difficulty_check;

 INSERT INTO users(id,username,password_hash) VALUES('89b6ef3a5cb57b6e04f74711d15a8a5f','tester','unused');`)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32), strings.Repeat("4", 32)}
	courses := []string{"Oni", "Tower", "Dan", "Oni"}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for i, id := range ids {
		_, err = tx.Exec(ctx, `INSERT INTO charts(id,owner_id,title,bpm,duration,encoding,wave_filename) VALUES($1,'89b6ef3a5cb57b6e04f74711d15a8a5f',$2,120,10,'utf-8','music.ogg')`, id, "Chart "+courses[i])
		if err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO chart_resources(chart_id,kind,storage_key,original_filename,sha256,byte_size,media_type) VALUES($1,'tja','chart.tja','chart.tja',repeat('a',64),1,'application/octet-stream'),($1,'audio','music.ogg','music.ogg',repeat('b',64),1,'audio/ogg')`, id)
		if err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, `UPDATE charts c SET difficulties=c.difficulties||d.items FROM (SELECT chart_id,jsonb_agg(jsonb_build_object('course',course,'level',level,'maker',maker)) items FROM (VALUES ($1,$2,5,'')) v(chart_id,course,level,maker) GROUP BY chart_id) d WHERE c.id=d.chart_id`, id, courses[i])
		if err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO scores(id,user_id,song_id,difficulty,good,ok,bad,score,drumroll,max_combo,payload_digest) VALUES($1,'89b6ef3a5cb57b6e04f74711d15a8a5f',$2,$3,10,0,0,10000,0,10,repeat('f',64))`, fmt.Sprintf("score%d", i), id, courses[i])
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = tx.Exec(ctx, `UPDATE charts c SET difficulties=c.difficulties||d.items FROM (SELECT chart_id,jsonb_agg(jsonb_build_object('course',course,'level',level,'maker',maker)) items FROM (VALUES ($1,'Dan',5,'')) v(chart_id,course,level,maker) GROUP BY chart_id) d WHERE c.id=d.chart_id`, ids[3])
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE charts c SET status='hidden' WHERE EXISTS(SELECT 1 FROM jsonb_array_elements(c.difficulties) d WHERE d->>'course' NOT IN ('Easy','Normal','Hard','Oni','Edit'));
SET CONSTRAINTS ALL IMMEDIATE;
ALTER TABLE charts ADD CONSTRAINT charts_difficulties_check CHECK(valid_chart_difficulties(difficulties,is_single)) NOT VALID;
ALTER TABLE scores ADD CONSTRAINT scores_difficulty_check CHECK(difficulty IN ('Easy','Normal','Hard','Oni','Edit')) NOT VALID;`); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		var status string
		err = pool.QueryRow(ctx, `SELECT status FROM charts WHERE id=$1`, id).Scan(&status)
		expected := "hidden"
		if i == 0 {
			expected = "published"
		}
		if err != nil || status != expected {
			t.Fatal("incorrect migration status", i, status, err)
		}
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM scores`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatal("migration removed scores")
	}
	for _, course := range []string{"Tower", "Dan"} {
		if _, err = pool.Exec(ctx, `UPDATE charts c SET difficulties=c.difficulties||d.items FROM (SELECT chart_id,jsonb_agg(jsonb_build_object('course',course,'level',level,'maker',maker)) items FROM (VALUES ($1,$2,5,'')) v(chart_id,course,level,maker) GROUP BY chart_id) d WHERE c.id=d.chart_id`, ids[0], course); err == nil {
			t.Fatal("database accepted unsupported course", course)
		}
	}
	if _, err = pool.Exec(ctx, `INSERT INTO scores(id,user_id,song_id,difficulty,good,ok,bad,score,drumroll,max_combo,payload_digest) VALUES('new-tower','89b6ef3a5cb57b6e04f74711d15a8a5f',$1,'Tower',10,0,0,10000,0,10,repeat('f',64))`, ids[1]); err == nil {
		t.Fatal("database accepted unsupported score")
	}
	// Even if a record is manually republished, API readers must not expose it.
	if _, err = pool.Exec(ctx, `ALTER TABLE charts DROP CONSTRAINT charts_difficulties_check; UPDATE charts SET status='published' WHERE status='hidden'; SET CONSTRAINTS ALL IMMEDIATE; ALTER TABLE charts ADD CONSTRAINT charts_difficulties_check CHECK(valid_chart_difficulties(difficulties,is_single)) NOT VALID;`); err != nil {
		t.Fatal(err)
	}
	cookie := strings.Repeat("c", 64)
	token := strings.Repeat("d", 64)
	if _, err = pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,'89b6ef3a5cb57b6e04f74711d15a8a5f','csrf',now()+interval '1 day'),($2,'89b6ef3a5cb57b6e04f74711d15a8a5f','csrf',now()+interval '1 day')`, hash(cookie), hash("game:"+token)); err != nil {
		t.Fatal(err)
	}
	const origin = "http://127.0.0.1:5173"
	handler := testServer(t, pool, Config{Origin: origin}).Handler()
	call := func(method, path, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", "csrf")
		if strings.HasPrefix(path, "/api/v1/game/") {
			r.Header.Set("Authorization", "Bearer "+token)
		} else {
			r.Header.Set("Origin", origin)
			r.AddCookie(&http.Cookie{Name: "ourtaiko_session", Value: cookie})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s: expected %d, got %d %s", path, status, w.Code, w.Body.String())
		}
		return w
	}
	for _, path := range []string{"/api/v1/charts", "/api/v1/me/charts", "/api/v1/charts?course=Oni"} {
		var list struct {
			Items []Chart
			Total int
		}
		w := call("GET", path, "", 200)
		if err = json.Unmarshal(w.Body.Bytes(), &list); err != nil || list.Total != 1 || len(list.Items) != 1 || list.Items[0].ID != ids[0] {
			t.Fatal("unsupported chart leaked in list", w.Body.String(), err)
		}
	}
	for _, course := range []string{"Tower", "Dan", "5", "6"} {
		call("GET", "/api/v1/charts?course="+course, "", 400)
		call("GET", "/api/v1/charts/"+ids[0]+"/leaderboard?difficulty="+course, "", 400)
		body := fmt.Sprintf(`{"songId":%q,"difficulty":%q,"good":10,"ok":0,"bad":0,"score":10000,"drumroll":0,"max_combo":10}`, ids[0], course)
		call("POST", "/api/v1/scores", body, 422)
		call("POST", "/api/v1/game/scores", body, 422)
	}
	for i := 1; i < len(ids); i++ {
		path := "/api/v1/charts/" + ids[i]
		call("GET", path, "", 404)
		call("GET", path+"/leaderboard", "", 404)
		for _, kind := range []string{"tja", "audio", "download"} {
			call("GET", path+"/"+kind, "", 404)
		}
		call("PATCH", path, `{"title":"Rename"}`, 404)
	}
	var snapshot struct {
		Charts []Chart
		Scores []Score
	}
	w := call("GET", "/api/v1/game/bootstrap", "", 200)
	if err = json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil || len(snapshot.Charts) != 0 || len(snapshot.Scores) != 1 || snapshot.Scores[0].Difficulty != "Oni" {
		t.Fatal("unsupported game data leaked", w.Body.String(), err)
	}
	// Fixtures already have the default Variety bit, including archived charts.
	w = call("GET", "/api/v1/game/categories/variety/charts", "", 200)
	if err = json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil || len(snapshot.Charts) != 1 || snapshot.Charts[0].ID != ids[0] {
		t.Fatal("unsupported chart leaked through category", w.Body.String(), err)
	}

}

func TestUploadRejectsUnsupportedCourses(t *testing.T) {
	pool := scoreTestDB(t)
	ctx := context.Background()
	cookie := strings.Repeat("a", 64)
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,password_hash) VALUES('89b6ef3a5cb57b6e04f74711d15a8a5f','tester','unused');`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,'89b6ef3a5cb57b6e04f74711d15a8a5f','csrf',now()+interval '1 day')`, hash(cookie)); err != nil {
		t.Fatal(err)
	}
	storage := t.TempDir()
	handler := testServer(t, pool, Config{Origin: "http://127.0.0.1:5173", Storage: storage}).Handler()
	for _, course := range []string{"Tower", "Dan", "5", "6", "tOwEr", "dAn"} {
		t.Run(course, func(t *testing.T) {
			var body bytes.Buffer
			form := multipart.NewWriter(&body)
			chart, _ := form.CreateFormFile("tja", "chart.tja")
			fmt.Fprintf(chart, "TITLE:Mixed\nBPM:120\nWAVE:music.ogg\nCOURSE:Oni\nLEVEL:5\n#START\n1000,\n#END\nCOURSE:%s\nLEVEL:5\n#START\n1000,\n#END\n", course)
			audio, _ := form.CreateFormFile("audio", "music.ogg")
			audio.Write([]byte("OggS fixture"))
			form.Close()
			r := httptest.NewRequest("POST", "/api/v1/charts", &body)
			r.Header.Set("Origin", "http://127.0.0.1:5173")
			r.Header.Set("Content-Type", form.FormDataContentType())
			r.Header.Set("X-CSRF-Token", "csrf")
			r.Header.Set("Idempotency-Key", "unsupported-course-"+course)
			r.AddCookie(&http.Cookie{Name: "ourtaiko_session", Value: cookie})
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 422 || !strings.Contains(w.Body.String(), "TJA_COURSE_UNSUPPORTED") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM charts`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("unsupported upload persisted")
	}
}
