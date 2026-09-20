package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
)

type User struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	Nickname      string `json:"nickname"`
	EmailVerified bool   `json:"emailVerified"`
	IsAdmin       bool   `json:"isAdmin"`
}
type session struct {
	User  User
	CSRF  string
	Token string
}
type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) localSession(r *http.Request) (session, error) {
	var v session
	c, e := r.Cookie("ourtaiko_session")
	if e != nil || len(c.Value) != 64 {
		return v, pgx.ErrNoRows
	}
	var encrypted []byte
	e = s.DB.QueryRow(r.Context(), `SELECT user_id,csrf_token,upstream_token FROM sessions WHERE token_hash=$1 AND expires_at>now()`, hash(c.Value)).Scan(&v.User.ID, &v.CSRF, &encrypted)
	if e != nil {
		return v, e
	}
	v.Token, e = s.Config.SSO.unseal(encrypted, hash(c.Value))
	return v, e
}
func (s *Server) ensureUser(ctx context.Context, u User) error {
	_, e := s.DB.Exec(ctx, `INSERT INTO users(id) VALUES($1) ON CONFLICT DO NOTHING`, u.ID)
	return e
}
func (s *Server) current(r *http.Request) (session, error) {
	var v session
	if isGameRequest(r) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || len(token) != 64 {
			return v, pgx.ErrNoRows
		}
		u, e := s.Config.SSO.identity(r.Context(), "game", token)
		if e != nil {
			return v, e
		}
		v.User = u
		return v, s.ensureUser(r.Context(), u)
	}
	v, e := s.localSession(r)
	if e != nil {
		return v, e
	}
	u, e := s.Config.SSO.identity(r.Context(), "web", v.Token)
	if e != nil {
		return session{}, e
	}
	if u.ID != v.User.ID {
		return session{}, pgx.ErrNoRows
	}
	v.User = u
	return v, nil
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
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !decode(w, r, &c) {
		return
	}
	var result struct {
		User    User   `json:"user"`
		Token   string `json:"accessToken"`
		Expires int    `json:"expiresIn"`
	}
	e := s.Config.SSO.call(r.Context(), "game/login", c, &result)
	var remote *ssoError
	if errors.As(e, &remote) {
		switch remote.Status {
		case 401:
			problem(w, 401, "LOGIN_INVALID", "用户名或密码不正确")
			return
		case 429:
			w.Header().Set("Retry-After", "600")
			problem(w, 429, "RATE_LIMITED", "尝试次数过多，请稍后重试")
			return
		case 400:
			problem(w, 400, "REQUEST_INVALID", "登录信息格式不正确")
			return
		}
	}
	if e != nil {
		internal(w, e)
		return
	}
	if !validSubject(result.User.ID) || len(result.Token) != 64 {
		internal(w, errors.New("invalid SSO login response"))
		return
	}
	if e = s.ensureUser(r.Context(), result.User); e != nil {
		internal(w, e)
		return
	}
	respond(w, 200, result)
}
func (s *Server) accountRedirect(w http.ResponseWriter, r *http.Request) {
	path := "/"
	if r.PathValue("page") == "register" {
		path = "/register/"
	}
	http.Redirect(w, r, s.Config.SSO.config.Issuer+path, http.StatusSeeOther)
}
func (s *Server) retiredAccountAPI(w http.ResponseWriter, r *http.Request) {
	problem(w, 410, "SSO_REQUIRED", "账号管理已迁至 OurTaiko 账号中心，请使用 SSO 登录或管理账号")
}
func safeReturn(raw string) string {
	u, e := url.Parse(raw)
	if e != nil || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.ContainsAny(raw, "\\\r\n") || u.IsAbs() || u.Host != "" {
		return "/upload"
	}
	return raw
}
func (s *Server) ssoLogin(w http.ResponseWriter, r *http.Request) {
	if dest := safeReturn(r.URL.Query().Get("returnTo")); strings.HasPrefix(dest, "/api/") {
		problem(w, 400, "RETURN_PATH_INVALID", "返回地址必须是网站页面")
		return
	}
	_, cfg, e := s.Config.SSO.oidc(r.Context())
	if e != nil {
		internal(w, e)
		return
	}
	state, binding, nonce, verifier := ID()+ID(), ID()+ID(), ID()+ID(), oauth2.GenerateVerifier()
	_, e = s.DB.Exec(r.Context(), `DELETE FROM oidc_flows WHERE expires_at<now()`)
	if e != nil {
		internal(w, e)
		return
	}
	_, e = s.DB.Exec(r.Context(), `INSERT INTO oidc_flows(state_hash,binding_hash,nonce,verifier,return_path,expires_at) VALUES($1,$2,$3,$4,$5,now()+interval '5 minutes')`, hash(state), hash(binding), nonce, verifier, safeReturn(r.URL.Query().Get("returnTo")))
	if e != nil {
		internal(w, e)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "ourtaiko_oidc", Value: binding, Path: "/api/v1/auth/sso", HttpOnly: true, Secure: s.Config.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: 300})
	http.Redirect(w, r, cfg.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusSeeOther)
}
func (s *Server) ssoCallback(w http.ResponseWriter, r *http.Request) {
	fail := func() { http.Redirect(w, r, s.Config.Origin+"/login?error=sso", http.StatusSeeOther) }
	c, e := r.Cookie("ourtaiko_oidc")
	state := r.URL.Query().Get("state")
	if e != nil || len(c.Value) != 64 || len(state) != 64 {
		fail()
		return
	}
	var nonce, verifier, destination string
	e = s.DB.QueryRow(r.Context(), `DELETE FROM oidc_flows WHERE state_hash=$1 AND binding_hash=$2 AND expires_at>now() RETURNING nonce,verifier,return_path`, hash(state), hash(c.Value)).Scan(&nonce, &verifier, &destination)
	http.SetCookie(w, &http.Cookie{Name: "ourtaiko_oidc", Path: "/api/v1/auth/sso", HttpOnly: true, Secure: s.Config.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	if e != nil || r.URL.Query().Get("error") != "" || r.URL.Query().Get("code") == "" {
		fail()
		return
	}
	provider, cfg, e := s.Config.SSO.oidc(r.Context())
	if e != nil {
		fail()
		return
	}
	ctx := oidc.ClientContext(r.Context(), s.Config.SSO.http)
	token, e := cfg.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(verifier))
	if e != nil {
		fail()
		return
	}
	revoke := func() {
		_ = s.Config.SSO.call(ctx, "web/revoke", map[string]string{"accessToken": token.AccessToken}, nil)
	}
	raw, _ := token.Extra("id_token").(string)
	idToken, e := provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}).Verify(ctx, raw)
	if e != nil || idToken.Nonce != nonce || !validSubject(idToken.Subject) {
		revoke()
		fail()
		return
	}
	if idToken.AccessTokenHash != "" && idToken.VerifyAccessToken(token.AccessToken) != nil {
		revoke()
		fail()
		return
	}
	u, e := s.Config.SSO.identity(ctx, "web", token.AccessToken)
	if e != nil || u.ID != idToken.Subject {
		revoke()
		fail()
		return
	}
	if e = s.ensureUser(ctx, u); e != nil {
		revoke()
		fail()
		return
	}
	rawSession, csrf := ID()+ID(), ID()+ID()
	digest := hash(rawSession)
	expires := token.Expiry
	if expires.IsZero() || expires.After(time.Now().Add(time.Hour)) {
		expires = time.Now().Add(time.Hour)
	}
	_, e = s.DB.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at,upstream_token) VALUES($1,$2,$3,$4,$5)`, digest, u.ID, csrf, expires, s.Config.SSO.seal(token.AccessToken, digest))
	if e != nil {
		revoke()
		fail()
		return
	}
	if old, e := r.Cookie("ourtaiko_session"); e == nil {
		_, _ = s.DB.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, hash(old.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: "ourtaiko_session", Value: rawSession, Path: "/", HttpOnly: true, Secure: s.Config.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: max(1, int(time.Until(expires).Seconds()))})
	http.Redirect(w, r, s.Config.Origin+safeReturn(destination), http.StatusSeeOther)
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	// Local logout still works when SSO is unavailable. Never require a remote refresh first.
	v, e := s.localSession(r)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		internal(w, e)
		return
	}
	if e == nil {
		if r.Header.Get("X-CSRF-Token") != v.CSRF {
			problem(w, 403, "CSRF_INVALID", "会话已更新，请刷新页面后重试")
			return
		}
		c, _ := r.Cookie("ourtaiko_session")
		if _, e = s.DB.Exec(r.Context(), `DELETE FROM sessions WHERE token_hash=$1`, hash(c.Value)); e != nil {
			internal(w, e)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		_ = s.Config.SSO.call(ctx, "web/revoke", map[string]string{"accessToken": v.Token}, nil)
	}
	http.SetCookie(w, &http.Cookie{Name: "ourtaiko_session", Path: "/", HttpOnly: true, Secure: s.Config.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	respond(w, 200, map[string]bool{"ok": true})
}
