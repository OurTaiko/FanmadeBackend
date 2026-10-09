package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicGameSearch(t *testing.T) {
	pool := scoreTestDB(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,nickname,password_hash) VALUES('eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','searcher','公开昵称','unused');`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 14; i++ {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		id := fmt.Sprintf("%032d", i)
		_, err = tx.Exec(ctx, `INSERT INTO charts(id,owner_id,title,bpm,duration,wave_filename,title_translations) VALUES($1,'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','Search Song',120,10,'test.ogg','{"ja":"検索曲"}')`, id)
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO chart_resources(chart_id,kind,storage_key,original_filename,sha256,byte_size,media_type) VALUES($1,'tja','tja','test.tja',repeat('a',64),1,'application/octet-stream'),($1,'audio','ogg','test.ogg',repeat('b',64),1,'audio/ogg')`, id)
		}

		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE charts c SET difficulties=c.difficulties||d.items FROM (SELECT chart_id,jsonb_agg(jsonb_build_object('course',course,'level',level,'maker',maker)) items FROM (VALUES ($1,'Easy',3,'Composer'),($1,'Oni',8,'Artist')) v(chart_id,course,level,maker) GROUP BY chart_id) d WHERE c.id=d.chart_id;`, id)
		}
		if err != nil {
			tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	s := testServer(t, pool, Config{Origin: "http://localhost"})
	handler := s.Handler()
	// The game filters by keyword, difficulty and star level in one bulk list;
	// the website pages a keyword search and ignores difficulty filters.
	for _, tc := range []struct {
		endpoint, query      string
		total, items, status int
	}{
		{"/api/v1/game/search", "?q=Search&course=Oni&level=8", 14, 14, 200},
		{"/api/v1/game/search", "?q=Search&course=Easy&level=8", 0, 0, 200},
		{"/api/v1/game/search", "?q=検索曲&level=3", 14, 14, 200},
		{"/api/v1/game/search", "?q=Artist", 14, 14, 200},
		{"/api/v1/game/search", "?q=公开昵称", 0, 0, 200},
		{"/api/v1/game/search", "?course=Edit", 0, 0, 200},
		{"/api/v1/game/search", "?course=Tower", 0, 0, 400},
		{"/api/v1/game/search", "?level=nope", 0, 0, 400},
		{"/api/v1/game/search", "?level=-1", 0, 0, 400},
		{"/api/v1/game/search", "?level=100", 0, 0, 400},
		{"/api/v1/game/search", "?order=hot", 0, 0, 400},
		{"/api/v1/game/search", "?order=comments", 0, 0, 400},
		{"/api/v1/charts", "?q=Search", 14, 12, 200},
		{"/api/v1/charts", "?q=Search&page=2", 14, 2, 200},
		{"/api/v1/charts", "?q=検索曲", 14, 12, 200},
		{"/api/v1/charts", "?q=Artist&order=hot", 14, 12, 200},
		{"/api/v1/charts", "?q=公开昵称", 0, 0, 200},
		{"/api/v1/charts", "?q=searcher", 0, 0, 200},
		{"/api/v1/charts", "?q=Search&course=Easy&level=8", 14, 12, 200},
		{"/api/v1/charts", "?course=Tower&level=nope", 14, 12, 200},
		{"/api/v1/charts", "?order=unfc", 0, 0, 400},
		{"/api/v1/charts", "?order=unperfect", 0, 0, 400},
		{"/api/v1/charts", "?page=10001", 0, 0, 400},
	} {
		r := httptest.NewRequest("GET", tc.endpoint+tc.query, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s%s: %d %s", tc.endpoint, tc.query, w.Code, w.Body.String())
		}
		if tc.status != 200 {
			continue
		}
		var result struct {
			Items []Chart `json:"items"`
			Total int     `json:"total"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Total != tc.total || len(result.Items) != tc.items {
			t.Fatalf("%s%s: got %d/%d", tc.endpoint, tc.query, result.Total, len(result.Items))
		}
	}
	// Completion ordering uses the current user's matching course, before pagination.
	for i := 1; i < 14; i++ {
		okCount := 0
		if i == 1 {
			okCount = 1
		}
		_, err := pool.Exec(ctx, `INSERT INTO scores(id,user_id,song_id,difficulty,good,ok,bad,score,drumroll,max_combo,payload_digest) VALUES($1,'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',$2,'Oni',10,$3,0,1000,0,10,repeat('a',64))`, fmt.Sprint("completed", i), fmt.Sprintf("%032d", i), okCount)
		if err != nil {
			t.Fatal(err)
		}
	}
	webToken := seedWebSession(t, s, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	gameToken := strings.Repeat("c", 64)
	if _, err := pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','',now()+interval '1 day')`, hash("game:"+gameToken)); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"/api/v1/game/search"} {
		for _, tc := range []struct {
			order, course, auth string
			first               int
		}{
			{"default", "Oni", "valid", 13},
			{"unfc", "Oni", "valid", 0},
			{"unperfect", "Oni", "valid", 1},
			{"unfc", "Easy", "valid", 13},
			{"unfc", "Oni", "", 13},
			{"unperfect", "Oni", "", 13},
			{"unfc", "Oni", "invalid", 13},
		} {
			r := httptest.NewRequest("GET", endpoint+"?order="+tc.order+"&course="+tc.course, nil)
			if tc.auth != "" {
				token := webToken
				if strings.HasPrefix(endpoint, "/api/v1/game/") {
					token = gameToken
				}
				if tc.auth == "invalid" {
					token = strings.Repeat("d", 64)
				}
				if strings.HasPrefix(endpoint, "/api/v1/game/") {
					r.Header.Set("Authorization", "Bearer "+token)
				} else {
					r.AddCookie(&http.Cookie{Name: "ourtaiko_session", Value: token})
				}
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			var result struct {
				Items []Chart `json:"items"`
				Total int     `json:"total"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Items) != 14 || result.Total != 14 || result.Items[0].ID != fmt.Sprintf("%032d", tc.first) {
				t.Fatalf("%s %+v: %d %s", endpoint, tc, w.Code, w.Body.String())
			}
		}
	}
	// Both default to newest first; the game list holds the whole paged web
	// list without web enrichment. Browser cookies cannot authorize game ordering.
	{
		var paged []string
		for _, path := range []string{"/api/v1/charts?page=1", "/api/v1/charts?page=2", "/api/v1/game/search?"} {
			r := httptest.NewRequest("GET", path+"&order=default", nil)
			if strings.HasPrefix(path, "/api/v1/game/") {
				r.Header.Set("Authorization", "Bearer "+gameToken)
			} else {
				r.AddCookie(&http.Cookie{Name: "ourtaiko_session", Value: webToken})
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			var result struct {
				Items    []Chart
				Total    int
				PageSize *int
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
				t.Fatal(w.Code, w.Body.String())
			}
			if strings.HasPrefix(path, "/api/v1/charts") {
				for _, c := range result.Items {
					paged = append(paged, c.ID)
				}
				continue
			}
			if result.PageSize != nil || len(result.Items) != len(paged) {
				t.Fatal("bulk response was paginated")
			}
			for i, c := range result.Items {
				if c.ID != paged[i] || c.Uploader != "" || c.CoverHash != "" {
					t.Fatal("bulk order or enrichment mismatch", c.ID)
				}
			}
		}
	}
	r := httptest.NewRequest("GET", "/api/v1/game/search?order=unfc&course=Oni", nil)
	r.AddCookie(&http.Cookie{Name: "ourtaiko_session", Value: webToken})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	var guest struct{ Items []Chart }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &guest) != nil || len(guest.Items) != 14 || guest.Items[0].ID != fmt.Sprintf("%032d", 13) {
		t.Fatal("browser cookie authorized game search", w.Body.String())
	}

	// Metadata search must keep working without SSO nickname search.
	s.Config.SSO.http = &http.Client{Transport: failingTransport{}}
	for _, endpoint := range []string{"/api/v1/charts", "/api/v1/game/search"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", endpoint+"?q=Artist", nil))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}

}
