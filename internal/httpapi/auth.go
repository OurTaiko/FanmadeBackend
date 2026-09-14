package httpapi

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

type User struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	EmailVerified bool   `json:"emailVerified"`
	IsAdmin       bool   `json:"isAdmin"`
}
type session struct {
	User User
	CSRF string
}

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_]{3,24}$`)

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) current(r *http.Request) (session, error) {
	var v session
	var digest string
	if isGameRequest(r) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || len(token) != 64 {
			return v, pgx.ErrNoRows
		}
		digest = hash("game:" + token)
	} else {
		c, e := r.Cookie("ourtaiko_session")
		if e != nil || len(c.Value) != 64 {
			return v, pgx.ErrNoRows
		}
		digest = hash(c.Value)
	}
	e := s.DB.QueryRow(r.Context(), `SELECT u.id,u.username,u.email_verified_at IS NOT NULL,u.is_admin,s.csrf_token FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>now()`, digest).Scan(&v.User.ID, &v.User.Username, &v.User.EmailVerified, &v.User.IsAdmin, &v.CSRF)
	return v, e
}
func (s *Server) required(w http.ResponseWriter, r *http.Request, csrf bool) (session, bool) {
	v, e := s.current(r)
	if e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			problem(w, 401, "UNAUTHORIZED", "请先登录")
		} else {
			internal(w, e)
		}
		return v, false
	}
	if csrf && !isGameRequest(r) && r.Header.Get("X-CSRF-Token") != v.CSRF {
		problem(w, 403, "CSRF_INVALID", "会话已更新，请刷新页面后重试")
		return v, false
	}
	return v, true
}
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	v, e := s.current(r)
	if errors.Is(e, pgx.ErrNoRows) {
		respond(w, 200, map[string]any{"user": nil, "csrfToken": ""})
		return
	}
	if e != nil {
		internal(w, e)
		return
	}
	respond(w, 200, map[string]any{"user": v.User, "csrfToken": v.CSRF})
}
func (s *Server) issue(w http.ResponseWriter, r *http.Request, u User) {
	token, csrf := ID()+ID(), ID()+ID()
	digest := hash(token)
	if isGameRequest(r) {
		digest = hash("game:" + token)
	}
	_, e := s.DB.Exec(r.Context(), `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,$2,$3,$4)`, digest, u.ID, csrf, time.Now().Add(7*24*time.Hour))
	if e != nil {
		internal(w, e)
		return
	}
	if isGameRequest(r) {
		respond(w, 200, map[string]any{"user": u, "accessToken": token, "expiresIn": 7 * 86400})
		return
	}
	if old, e := r.Cookie("ourtaiko_session"); e == nil {
		s.DB.Exec(r.Context(), `DELETE FROM sessions WHERE token_hash=$1`, hash(old.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: "ourtaiko_session", Value: token, Path: "/", HttpOnly: true, Secure: s.Config.CookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: 7 * 86400})
	respond(w, 200, map[string]any{"user": u, "csrfToken": csrf})
}

var dummyHash = func() []byte { h, _ := bcrypt.GenerateFromPassword([]byte("dummy-account-password"), 12); return h }()

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !decode(w, r, &c) {
		return
	}
	var u User
	var h string
	e := s.DB.QueryRow(r.Context(), `SELECT id,username,password_hash,email_verified_at IS NOT NULL,is_admin FROM users WHERE username=$1`, c.Username).Scan(&u.ID, &u.Username, &h, &u.EmailVerified, &u.IsAdmin)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		internal(w, e)
		return
	}
	candidate := []byte(h)
	if e != nil {
		candidate = dummyHash
	}
	passwordErr := bcrypt.CompareHashAndPassword(candidate, []byte(c.Password))
	if e != nil || passwordErr != nil {
		problem(w, 401, "LOGIN_INVALID", "用户名或密码不正确")
		return
	}
	s.issue(w, r, u)
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.required(w, r, true); !ok {
		return
	}
	c, _ := r.Cookie("ourtaiko_session")
	if _, e := s.DB.Exec(r.Context(), `DELETE FROM sessions WHERE token_hash=$1`, hash(c.Value)); e != nil {
		internal(w, e)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "ourtaiko_session", Value: "", Path: "/", HttpOnly: true, Secure: s.Config.CookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	respond(w, 200, map[string]bool{"ok": true})
}
