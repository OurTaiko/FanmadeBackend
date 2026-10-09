package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testReplay = `{"version":1,"audio_offset_ms":-20,"visual_offset_ms":10,"inputs":[[-15.5,0],[1234.5,1],[1234.5,2],[1200,3]]}`

func TestNormalizeScoreReplay(t *testing.T) {
	for _, raw := range []string{testReplay, strings.Replace(testReplay, `[-15.5,0],[1234.5,1],[1234.5,2],[1200,3]`, "", 1)} {
		if normalized := normalizeScoreReplay(json.RawMessage(raw)); string(normalized) != raw {
			t.Fatalf("lost event order, simultaneous inputs, offsets or empty recording: %s", normalized)
		}
	}
	for name, raw := range map[string]string{
		"absent": "", "null": "null", "array": "[]", "string": `"invalid recording"`,
		"malformed": "{", "missing version": `{"inputs":[]}`,
		"future version":    strings.Replace(testReplay, `"version":1`, `"version":2`, 1),
		"missing audio":     strings.Replace(testReplay, `"audio_offset_ms":-20,`, "", 1),
		"null visual":       strings.Replace(testReplay, `"visual_offset_ms":10`, `"visual_offset_ms":null`, 1),
		"fractional offset": strings.Replace(testReplay, `"visual_offset_ms":10`, `"visual_offset_ms":1.5`, 1),
		"overflow offset":   strings.Replace(testReplay, `"visual_offset_ms":10`, `"visual_offset_ms":2147483648`, 1),
		"missing inputs":    `{"version":1,"audio_offset_ms":0,"visual_offset_ms":0}`,
		"null inputs":       `{"version":1,"audio_offset_ms":0,"visual_offset_ms":0,"inputs":null}`,
		"object inputs":     `{"version":1,"audio_offset_ms":0,"visual_offset_ms":0,"inputs":{"100":1}}`,
		"null event":        strings.Replace(testReplay, `[-15.5,0]`, `null`, 1),
		"short event":       strings.Replace(testReplay, `[-15.5,0]`, `[1]`, 1),
		"long event":        strings.Replace(testReplay, `[-15.5,0]`, `[1,2,3]`, 1),
		"null time":         strings.Replace(testReplay, `[-15.5,0]`, `[null,0]`, 1),
		"string time":       strings.Replace(testReplay, `[-15.5,0]`, `["10",0]`, 1),
		"time overflow":     strings.Replace(testReplay, `[-15.5,0]`, `[1e999,0]`, 1),
		"time range":        strings.Replace(testReplay, `[-15.5,0]`, `[86400001,0]`, 1),
		"null key":          strings.Replace(testReplay, `[-15.5,0]`, `[1,null]`, 1),
		"fractional key":    strings.Replace(testReplay, `[-15.5,0]`, `[1,1.5]`, 1),
		"negative key":      strings.Replace(testReplay, `[-15.5,0]`, `[1,-1]`, 1),
		"key range":         strings.Replace(testReplay, `[-15.5,0]`, `[1,4]`, 1),
		"unknown field":     strings.TrimSuffix(testReplay, "}") + `,"unsupported":true}`,
		"too large":         `"` + strings.Repeat("x", maxReplayBytes) + `"`,
		"too many events":   `{"version":1,"audio_offset_ms":0,"visual_offset_ms":0,"inputs":[` + strings.Repeat(`[0,1],`, maxReplayInputs) + `[0,1]]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if normalizeScoreReplay(json.RawMessage(raw)) != nil {
				t.Fatal("invalid recording must normalize to SQL NULL")
			}
		})
	}
}

func TestScoreReplayCompatibility(t *testing.T) {
	pool := scoreTestDB(t)
	ctx := context.Background()
	const user = "89b6ef3a5cb57b6e04f74711d15a8a5f"
	const song = "11111111111111111111111111111111"
	const token = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	const origin = "http://localhost:5173"
	const base = `{"songId":"` + song + `","difficulty":"Oni","good":10,"ok":0,"bad":0,"score":10000,"drumroll":0,"max_combo":10}`
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO users(id,username,password_hash) VALUES($1,'replaytest','unused');`, user)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO charts(id,owner_id,title,bpm,duration,wave_filename) VALUES('11111111111111111111111111111111','89b6ef3a5cb57b6e04f74711d15a8a5f','Recording fixture',120,10,'test.ogg');
 INSERT INTO chart_resources(chart_id,kind,storage_key,original_filename,sha256,byte_size,media_type) VALUES
 ('11111111111111111111111111111111','tja','tja','test.tja',repeat('a',64),1,'application/octet-stream'),('11111111111111111111111111111111','audio','ogg','test.ogg',repeat('b',64),1,'audio/ogg');
 UPDATE charts c SET difficulties=c.difficulties||d.items FROM (SELECT chart_id,jsonb_agg(jsonb_build_object('course',course,'level',level,'maker',maker)) items FROM (VALUES ('11111111111111111111111111111111','Oni',5,'')) v(chart_id,course,level,maker) GROUP BY chart_id) d WHERE c.id=d.chart_id;`)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	app := testServer(t, pool, Config{Origin: origin})
	_, err = pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,$2,'',now()+interval '1 day')`, hash("game:"+token), user)
	if err != nil {
		t.Fatal(err)
	}
	cookie := seedWebSession(t, app, user)
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	call := func(path, body, key string, want int) Score {
		t.Helper()
		req, err := http.NewRequest("POST", server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		if strings.Contains(path, "/game/") {
			req.Header.Set("Authorization", "Bearer "+token)
		} else {
			req.Header.Set("Origin", origin)
			req.Header.Set("X-CSRF-Token", "csrf")
			req.AddCookie(&http.Cookie{Name: "ourtaiko_session", Value: cookie})
		}
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		if response.StatusCode != want {
			t.Fatalf("want %d got %d: %s", want, response.StatusCode, data)
		}
		var score Score
		if want < 300 && (json.Unmarshal(data, &score) != nil || score.ID == "" || score.Score != 10000) {
			t.Fatal("bad score receipt")
		}
		if strings.Contains(string(data), "replay_data") {
			t.Fatal("recording must not inflate score receipts")
		}
		return score
	}
	attach := func(raw string) string { return strings.TrimSuffix(base, "}") + `,"replay_data":` + raw + "}" }
	for index, path := range []string{"/api/v1/game/scores", "/api/v1/scores"} {
		key := fmt.Sprintf("recording-legacy-%04d", index)
		legacy := call(path, base, key, 201)
		var digest string
		var isNull bool
		if err := pool.QueryRow(ctx, `SELECT replay_data IS NULL,payload_digest FROM scores WHERE id=$1`, legacy.ID).Scan(&isNull, &digest); err != nil || !isNull || digest != hash(base) {
			t.Fatalf("legacy payload/digest changed: null=%v err=%v", isNull, err)
		}
		// A retry after upgrading the server must match an old request exactly.
		if retry := call(path, base, key, 200); retry.ID != legacy.ID || retry.ClearStatus != 0 {
			t.Fatal("duplicated legacy score")
		}
		if retry := call(path, strings.TrimSuffix(base, "}")+`,"ClearStatus":0}`, key, 200); retry.ID != legacy.ID || retry.ClearStatus != 0 {
			t.Fatal("explicit zero broke legacy idempotency")
		}
		for _, raw := range []string{"null", `"broken"`, `{}`, strings.Replace(testReplay, `[-15.5,0]`, `[1,9]`, 1)} {
			if retry := call(path, attach(raw), key, 200); retry.ID != legacy.ID {
				t.Fatal("invalid replay broke legacy idempotency")
			}
			bad := call(path, attach(raw), "", 201)
			if err := pool.QueryRow(ctx, `SELECT replay_data IS NULL FROM scores WHERE id=$1`, bad.ID).Scan(&isNull); err != nil || !isNull {
				t.Fatal("invalid replay was not SQL NULL", err)
			}
		}
		validKey := fmt.Sprintf("recording-valid-%04d", index)
		valid := call(path, attach(testReplay), validKey, 201)
		var equal bool
		if err := pool.QueryRow(ctx, `SELECT replay_data=$2::jsonb FROM scores WHERE id=$1`, valid.ID, testReplay).Scan(&equal); err != nil || !equal {
			t.Fatal("recording lost events or delay settings", err)
		}
		if retry := call(path, attach(strings.Replace(testReplay, "1234.5", "1234.500", -1)), validKey, 200); retry.ID != valid.ID {
			t.Fatal("canonical retry duplicated score")
		}
		call(path, attach(strings.Replace(testReplay, `"audio_offset_ms":-20`, `"audio_offset_ms":-21`, 1)), validKey, 409)
		call(path, attach(strings.Replace(testReplay, `[1234.5,2]`, `[1234.5,3]`, 1)), validKey, 409)
	}
	// Requests larger than the old 8 KB cap are valid. Bound attachment size
	// independently so an oversized recording still saves the basic score.
	large := `{"version":1,"audio_offset_ms":0,"visual_offset_ms":0,"inputs":[` + strings.Repeat(`[1234.5,1],`, 5000) + `[1234.5,2]]}`
	score := call("/api/v1/game/scores", attach(large), "", 201)
	var length int
	if err := pool.QueryRow(ctx, `SELECT jsonb_array_length(replay_data->'inputs') FROM scores WHERE id=$1`, score.ID).Scan(&length); err != nil || length != 5001 {
		t.Fatal("large recording was lost", err)
	}
	oversized := call("/api/v1/game/scores", attach(`"`+strings.Repeat("x", maxReplayBytes)+`"`), "", 201)
	var isNull bool
	if err := pool.QueryRow(ctx, `SELECT replay_data IS NULL FROM scores WHERE id=$1`, oversized.ID).Scan(&isNull); err != nil || !isNull {
		t.Fatal("oversized attachment must become NULL", err)
	}
	call("/api/v1/game/scores", attach(`"`+strings.Repeat("x", maxScoreRequestBytes)+`"`), "", 413)
	call("/api/v1/game/scores", strings.TrimSuffix(base, "}")+`,"replay_data":{`, "", 400)
	req, _ := http.NewRequest("GET", server.URL+"/api/v1/game/bootstrap", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 || !strings.Contains(string(data), `"scoreReplayVersion":1`) || strings.Contains(string(data), "replay_data") {
		t.Fatal("bootstrap capability/score compatibility failed")
	}
}
