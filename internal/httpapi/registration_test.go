package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

type testMailbox struct {
	mu    sync.Mutex
	codes map[string]string
	fail  bool
}

func (m *testMailbox) SendRegistration(_ context.Context, email, code string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("simulated SMTP failure")
	}
	m.codes[email] = code
	return nil
}
func (m *testMailbox) code(email string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.codes[email]
}

func TestRegistrationEmail(t *testing.T) {
	pool := scoreTestDB(t)
	ctx := context.Background()
	box := &testMailbox{codes: map[string]string{}}
	const origin = "http://127.0.0.1:5173"
	handler := New(pool, Config{Origin: origin, Mailer: box}).Handler()
	var ip atomic.Uint32
	call := func(path string, body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "/api/v1/auth/"+path, strings.NewReader(string(b)))
		r.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", ip.Add(1))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	assert := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		if w.Code != status || (code != "" && !strings.Contains(w.Body.String(), code)) {
			t.Fatalf("want %d %s, got %d %s", status, code, w.Code, w.Body.String())
		}
	}
	send := func(email string) string {
		t.Helper()
		w := call("email-code", map[string]string{"email": email})
		assert(w, 200, "verificationId")
		var reply struct {
			VerificationID string `json:"verificationId"`
			RetryAfter     int    `json:"retryAfter"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || reply.RetryAfter != 60 {
			t.Fatal(w.Body.String(), err)
		}
		if strings.Contains(w.Body.String(), box.code(strings.ToLower(strings.TrimSpace(email)))) {
			t.Fatal("response leaked code")
		}
		return reply.VerificationID
	}
	payload := func(name, email, id, code string) registration {
		return registration{Username: name, Password: "test-password-123", Email: email, VerificationID: id, Code: code}
	}
	assert(call("email-code", map[string]string{"email": "Name <person@example.com>"}), 422, "EMAIL_INVALID")
	assert(call("email-code", map[string]string{"email": "person@example.com\r\nBcc: other@example.com"}), 422, "EMAIL_INVALID")
	assert(call("register", map[string]string{"username": "noemail", "password": "test-password-123"}), 422, "EMAIL_INVALID")
	id := send(" Person@Example.com ")
	code := box.code("person@example.com")
	if !codePattern.MatchString(code) {
		t.Fatal("invalid generated code")
	}
	var digest string
	if err := pool.QueryRow(ctx, `SELECT code_hash FROM registration_codes WHERE email='person@example.com'`).Scan(&digest); err != nil || digest == code || bcrypt.CompareHashAndPassword([]byte(digest), []byte(id+":"+code)) != nil {
		t.Fatal("code was not hashed correctly", err)
	}
	w := call("email-code", map[string]string{"email": "PERSON@example.com"})
	assert(w, 429, "EMAIL_RATE_LIMITED")
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("missing retry delay")
	}
	p := payload("newuser", "PERSON@example.com", id, "")
	assert(call("register", p), 422, "EMAIL_CODE_INVALID")
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	p.Code = wrong
	assert(call("register", p), 422, "EMAIL_CODE_INVALID")
	var attempts, users int
	pool.QueryRow(ctx, `SELECT attempts FROM registration_codes WHERE email='person@example.com'`).Scan(&attempts)
	pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&users)
	if attempts != 1 || users != 0 {
		t.Fatal("wrong code changed account state", attempts, users)
	}
	p.Email = "different@example.com"
	p.Code = code
	assert(call("register", p), 422, "EMAIL_CODE_INVALID")
	p.Email = "PERSON@example.com"
	w = call("register", p)
	assert(w, 200, `"emailVerified":true`)
	if len(w.Result().Cookies()) != 1 {
		t.Fatal("registration did not issue a session")
	}
	var savedEmail, passwordHash string
	var verified bool
	if err := pool.QueryRow(ctx, `SELECT email,email_verified_at IS NOT NULL,password_hash FROM users WHERE username='newuser'`).Scan(&savedEmail, &verified, &passwordHash); err != nil || savedEmail != "person@example.com" || !verified || bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(p.Password)) != nil {
		t.Fatal("bad user record", err)
	}
	p.Username = "replayed"
	assert(call("register", p), 422, "EMAIL_CODE_INVALID")
	assert(call("email-code", map[string]string{"email": "PERSON@example.com"}), 409, "EMAIL_EXISTS")
	assert(call("login", credentials{Username: "newuser", Password: p.Password}), 200, `"emailVerified":true`)

	t.Run("expired", func(t *testing.T) {
		id := send("expired@example.com")
		if _, err := pool.Exec(ctx, `UPDATE registration_codes SET expires_at=now()-interval '1 second' WHERE email='expired@example.com'`); err != nil {
			t.Fatal(err)
		}
		assert(call("register", payload("expired", "expired@example.com", id, box.code("expired@example.com"))), 422, "EMAIL_CODE_EXPIRED")
	})
	t.Run("five attempts persist across restart", func(t *testing.T) {
		id := send("locked@example.com")
		code := box.code("locked@example.com")
		wrong := "000000"
		if wrong == code {
			wrong = "111111"
		}
		p := payload("locked", "locked@example.com", id, wrong)
		for n := 0; n < 4; n++ {
			assert(call("register", p), 422, "EMAIL_CODE_INVALID")
		}
		assert(call("register", p), 422, "EMAIL_CODE_LOCKED")
		handler = New(pool, Config{Origin: origin, Mailer: box}).Handler()
		p.Code = code
		assert(call("register", p), 422, "EMAIL_CODE_LOCKED")
	})
	t.Run("resend invalidates previous challenge and email hourly cap", func(t *testing.T) {
		email := "resend@example.com"
		oldID := send(email)
		oldCode := box.code(email)
		for n := 1; n < 5; n++ {
			if _, err := pool.Exec(ctx, `UPDATE registration_codes SET sent_at=now()-interval '61 seconds' WHERE email=$1`, email); err != nil {
				t.Fatal(err)
			}
			send(email)
		}
		assert(call("register", payload("resend", email, oldID, oldCode)), 422, "EMAIL_CODE_INVALID")
		if _, err := pool.Exec(ctx, `UPDATE registration_codes SET sent_at=now()-interval '61 seconds' WHERE email=$1`, email); err != nil {
			t.Fatal(err)
		}
		assert(call("email-code", map[string]string{"email": email}), 429, "EMAIL_RATE_LIMITED")
	})
	t.Run("SMTP failure preserves previous challenge", func(t *testing.T) {
		email := "failure@example.com"
		id := send(email)
		code := box.code(email)
		if _, err := pool.Exec(ctx, `UPDATE registration_codes SET sent_at=now()-interval '61 seconds' WHERE email=$1`, email); err != nil {
			t.Fatal(err)
		}
		box.fail = true
		assert(call("email-code", map[string]string{"email": email}), 503, "EMAIL_DELIVERY_FAILED")
		assert(call("email-code", map[string]string{"email": "never-sent@example.com"}), 503, "EMAIL_DELIVERY_FAILED")
		box.fail = false
		assert(call("register", payload("sentbefore", email, id, code)), 200, `"emailVerified":true`)
		var count int
		pool.QueryRow(ctx, `SELECT count(*) FROM registration_codes WHERE email='never-sent@example.com'`).Scan(&count)
		if count != 0 {
			t.Fatal("failed send left usable challenge")
		}
	})
	t.Run("username conflict does not consume code", func(t *testing.T) {
		email := "conflict@example.com"
		id := send(email)
		p := payload("newuser", email, id, box.code(email))
		assert(call("register", p), 409, "USERNAME_EXISTS")
		p.Username = "renamed"
		assert(call("register", p), 200, `"emailVerified":true`)
	})
	t.Run("concurrent use creates one account", func(t *testing.T) {
		email := "concurrent@example.com"
		id := send(email)
		results := make(chan int, 2)
		for _, name := range []string{"concurrent1", "concurrent2"} {
			go func(name string) { results <- call("register", payload(name, email, id, box.code(email))).Code }(name)
		}
		a, b := <-results, <-results
		if !((a == 200 && b == 422) || (b == 200 && a == 422)) {
			t.Fatal(a, b)
		}
	})
	t.Run("IP limit persists across restart", func(t *testing.T) {
		_, err := pool.Exec(ctx, `INSERT INTO email_send_limits(ip_hash,window_started_at,send_count) VALUES($1,now(),10)`, hash("198.51.100.1"))
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/api/v1/auth/email-code", strings.NewReader(`{"email":"limited@example.com"}`))
		r.RemoteAddr = "198.51.100.1:1234"
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		New(pool, Config{Origin: origin, Mailer: box}).Handler().ServeHTTP(w, r)
		assert(w, 429, "EMAIL_RATE_LIMITED")
	})
	t.Run("no mailer cannot bypass verification", func(t *testing.T) {
		handler = New(pool, Config{Origin: origin}).Handler()
		assert(call("email-code", map[string]string{"email": "unavailable@example.com"}), 503, "EMAIL_DELIVERY_UNAVAILABLE")
	})
}
