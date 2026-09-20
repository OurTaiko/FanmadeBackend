package httpapi

import (
	"context"
	"encoding/json"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGameSessionIsolation(t *testing.T) {
	pool := scoreTestDB(t)
	h, _ := bcrypt.GenerateFromPassword([]byte("test-password"), bcrypt.MinCost)
	_, err := pool.Exec(context.Background(), `INSERT INTO users(id,username,password_hash) VALUES('game-user','gameuser',$1)`, string(h))
	if err != nil {
		t.Fatal(err)
	}
	handler := New(pool, Config{Origin: "http://localhost:5173"}).Handler()
	call := func(method, path, body, token, origin, cookie string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "ourtaiko_session", Value: cookie})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	credentials := `{"username":"gameuser","password":"test-password"}`
	login := call("POST", "/api/v1/game/login", credentials, "", "", "")
	if login.Code != 200 {
		t.Fatal(login.Code, login.Body.String())
	}
	var result struct{ AccessToken string }
	if err := json.Unmarshal(login.Body.Bytes(), &result); err != nil || len(result.AccessToken) != 64 || len(login.Result().Cookies()) != 0 {
		t.Fatal("invalid native session")
	}
	bootstrap := call("GET", "/api/v1/game/bootstrap", "", result.AccessToken, "", "")
	if bootstrap.Code != 200 || !strings.Contains(bootstrap.Body.String(), `"scores":[]`) {
		t.Fatal(bootstrap.Code, bootstrap.Body.String())
	}
	for _, c := range []struct {
		method, path, body, token, origin, cookie string
		status                                    int
	}{
		{"GET", "/api/v1/game/bootstrap", "", "", "", result.AccessToken, 200},
		{"GET", "/api/v1/game/bootstrap", "", "invalid", "", "", 401},
		{"GET", "/api/v1/me/charts", "", result.AccessToken, "", "", 401},
		{"GET", "/api/v1/me/charts", "", "", "", result.AccessToken, 401},
		{"POST", "/api/v1/game/login", credentials, "", "https://evil.test", "", 403},
		{"POST", "/api/v1/game/scores", "{}", "", "", "", 401},
		{"POST", "/api/v1/game/scores", "{}", result.AccessToken, "", "", 422},
	} {
		if w := call(c.method, c.path, c.body, c.token, c.origin, c.cookie); w.Code != c.status {
			t.Errorf("%s: want %d got %d %s", c.path, c.status, w.Code, w.Body.String())
		}
	}
	web := call("POST", "/api/v1/auth/login", credentials, "", "http://localhost:5173", "")
	if web.Code != 200 {
		t.Fatal(web.Body.String())
	}
	webToken := web.Result().Cookies()[0].Value
	for _, cookie := range []string{"", result.AccessToken, webToken} {
		w := call("GET", "/api/v1/game/bootstrap", "", "", "", cookie)
		var guest struct {
			User       *User
			Scores     []Score
			Categories []Category
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &guest) != nil || guest.User != nil || guest.Scores == nil || len(guest.Scores) != 0 || len(guest.Categories) == 0 {
			t.Fatal("guest bootstrap must expose catalog without identity or scores", w.Code, w.Body.String())
		}
		if w = call("POST", "/api/v1/game/scores", "{}", "", "", cookie); w.Code != 401 {
			t.Fatal("guest score submission accepted", w.Code, w.Body.String())
		}
	}
	if w := call("GET", "/api/v1/game/bootstrap", "", webToken, "", ""); w.Code != 401 {
		t.Fatal("browser token accepted as native token")
	}
	if _, err = pool.Exec(context.Background(), `UPDATE sessions SET expires_at=now()-interval '1 second' WHERE token_hash=$1`, hash("game:"+result.AccessToken)); err != nil {
		t.Fatal(err)
	}
	if w := call("GET", "/api/v1/game/bootstrap", "", result.AccessToken, "", ""); w.Code != 401 {
		t.Fatal("expired token must request reauthentication", w.Code, w.Body.String())
	}
}
