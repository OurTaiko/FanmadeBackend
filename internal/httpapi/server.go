package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"ourtaiko.dev/fanmade/api/internal/tja"
)

type RegistrationMailer interface {
	SendRegistration(context.Context, string, string) error
}

type Config struct {
	Origin, Storage string
	CookieSecure    bool
	Mailer          RegistrationMailer
	TrustedProxies  []netip.Prefix
}
type Server struct {
	DB         *pgxpool.Pool
	Config     Config
	uploads    chan struct{}
	emailSends chan struct{}
	mu         sync.Mutex
	limits     map[string]window
}
type window struct {
	since time.Time
	count int
}

func New(pool *pgxpool.Pool, cfg Config) *Server {
	return &Server{DB: pool, Config: cfg, uploads: make(chan struct{}, 2), emailSends: make(chan struct{}, 2), limits: map[string]window{}}
}
func ID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, status int, code, msg string) {
	respond(w, status, map[string]any{"code": code, "message": msg, "requestId": w.Header().Get("X-Request-ID"), "validationVersion": tja.Version})
}
func internal(w http.ResponseWriter, err error) {
	log.Printf("request=%s error=%v", w.Header().Get("X-Request-ID"), err)
	problem(w, 503, "SERVICE_UNAVAILABLE", "服务暂时不可用，请稍后重试")
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		problem(w, 415, "CONTENT_TYPE_INVALID", "请使用 JSON 请求")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		problem(w, 400, "REQUEST_INVALID", "请求格式不正确")
		return false
	}
	return true
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, map[string]bool{"ok": true}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if e := s.DB.Ping(ctx); e != nil {
			internal(w, e)
			return
		}
		respond(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/v1/upload-rules", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]any{"validationVersion": tja.Version, "maxTjaBytes": tja.MaxTJA, "maxAudioBytes": tja.MaxAudio, "encodings": []string{"utf-8", "shift-jis"}, "audioCodecs": []string{"vorbis"}})
	})
	mux.HandleFunc("POST /api/v1/game/login", s.login)
	mux.HandleFunc("GET /api/v1/game/bootstrap", s.gameBootstrap)
	mux.HandleFunc("POST /api/v1/game/scores", s.submitScore)
	mux.HandleFunc("POST /api/v1/auth/email-code", s.sendEmailCode)
	mux.HandleFunc("POST /api/v1/auth/register", s.register)
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("POST /api/v1/auth/logout", s.logout)
	mux.HandleFunc("GET /api/v1/me", s.me)
	mux.HandleFunc("POST /api/v1/scores", s.submitScore)
	mux.HandleFunc("GET /api/v1/charts", s.list)
	mux.HandleFunc("GET /api/v1/me/charts", s.mine)
	mux.HandleFunc("POST /api/v1/charts", s.upload)
	mux.HandleFunc("GET /api/v1/charts/{id}", s.detail)
	mux.HandleFunc("GET /api/v1/charts/{id}/leaderboard", s.leaderboard)
	mux.HandleFunc("PATCH /api/v1/charts/{id}", s.editMetadata)
	mux.HandleFunc("DELETE /api/v1/charts/{id}", s.remove)
	mux.HandleFunc("GET /api/v1/charts/{id}/versions/{version}/{kind}", s.download)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", ID())
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "GET" && r.Method != "HEAD" {
			if (isGameRequest(r) && r.Header.Get("Origin") != "") || (!isGameRequest(r) && r.Header.Get("Origin") != s.Config.Origin) {
				problem(w, 403, "ORIGIN_INVALID", "请求来源无效，请从本站页面操作")
				return
			}
			host := s.clientIP(r)
			s.mu.Lock()
			now := time.Now()
			for k, v := range s.limits {
				if now.Sub(v.since) > time.Minute {
					delete(s.limits, k)
				}
			}
			limit := s.limits[host]
			if limit.since.IsZero() {
				limit.since = now
			}
			limit.count++
			s.limits[host] = limit
			s.mu.Unlock()
			if limit.count > 40 {
				problem(w, 429, "RATE_LIMITED", "操作过于频繁，请稍后再试")
				return
			}
		}
		defer func() {
			if e := recover(); e != nil {
				log.Printf("panic: %v", e)
				problem(w, 500, "INTERNAL_ERROR", "服务内部错误")
			}
		}()
		mux.ServeHTTP(w, r)
	})
}
func (s *Server) EnsureStorage() error {
	if s.Config.Storage == "" {
		return errors.New("storage directory required")
	}
	return os.MkdirAll(s.Config.Storage, 0700)
}
