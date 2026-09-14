package httpapi

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
)

const codeLifetime = 10 * time.Minute
const resendDelay = time.Minute

var codePattern = regexp.MustCompile(`^[0-9]{6}$`)

func normalizeEmail(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) > 254 || strings.ContainsAny(value, "\r\n") {
		return "", false
	}
	for _, c := range value {
		if c > 127 {
			return "", false
		}
	}
	addr, err := mail.ParseAddress(value)
	if err != nil || addr.Address != value {
		return "", false
	}
	local, domain, ok := strings.Cut(value, "@")
	if !ok || len(local) > 64 || !strings.Contains(domain, ".") {
		return "", false
	}
	return value, true
}

func emailLock(ctx context.Context, tx pgx.Tx, email string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('registration:' || $1, 0))`, email)
	return err
}
func retryLater(w http.ResponseWriter, seconds int, message string) {
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	problem(w, 429, "EMAIL_RATE_LIMITED", message)
}

func (s *Server) sendEmailCode(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email string `json:"email"`
	}
	if !decode(w, r, &input) {
		return
	}
	email, ok := normalizeEmail(input.Email)
	if !ok {
		problem(w, 422, "EMAIL_INVALID", "请输入有效的邮箱地址")
		return
	}
	if s.Config.Mailer == nil {
		problem(w, 503, "EMAIL_DELIVERY_UNAVAILABLE", "邮件服务暂不可用，请稍后重试")
		return
	}
	select {
	case s.emailSends <- struct{}{}:
		defer func() { <-s.emailSends }()
	default:
		retryLater(w, 5, "邮件发送繁忙，请稍后重试")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	// Persist the IP budget independently so SMTP failures and process restarts do not reset it.
	host := s.clientIP(r)
	var count int
	var windowStart time.Time
	err := s.DB.QueryRow(ctx, `INSERT INTO email_send_limits(ip_hash,window_started_at,send_count) VALUES($1,now(),1)
 ON CONFLICT(ip_hash) DO UPDATE SET
 send_count=CASE WHEN email_send_limits.window_started_at <= now()-interval '1 hour' THEN 1 ELSE LEAST(email_send_limits.send_count+1,11) END,
 window_started_at=CASE WHEN email_send_limits.window_started_at <= now()-interval '1 hour' THEN now() ELSE email_send_limits.window_started_at END
 RETURNING send_count,window_started_at`, hash(host)).Scan(&count, &windowStart)
	if err != nil {
		internal(w, err)
		return
	}
	if count > 10 {
		retryLater(w, int(time.Until(windowStart.Add(time.Hour)).Seconds())+1, "该网络发送次数过多，请一小时后再试")
		return
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback(ctx)
	if err = emailLock(ctx, tx, email); err != nil {
		internal(w, err)
		return
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE lower(email)=$1)`, email).Scan(&exists); err != nil {
		internal(w, err)
		return
	}
	if exists {
		problem(w, 409, "EMAIL_EXISTS", "该邮箱已注册，请直接登录")
		return
	}
	var lastSent time.Time
	var sentCount int
	err = tx.QueryRow(ctx, `SELECT sent_at,window_started_at,send_count FROM registration_codes WHERE email=$1`, email).Scan(&lastSent, &windowStart, &sentCount)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		internal(w, err)
		return
	}
	now := time.Now()
	if err == nil && now.Before(lastSent.Add(resendDelay)) {
		retryLater(w, int(time.Until(lastSent.Add(resendDelay)).Seconds())+1, "验证码已发送，请稍后再获取")
		return
	}
	if err == nil && now.Before(windowStart.Add(time.Hour)) && sentCount >= 5 {
		retryLater(w, int(time.Until(windowStart.Add(time.Hour)).Seconds())+1, "该邮箱发送次数过多，请一小时后再试")
		return
	}
	if err != nil || !now.Before(windowStart.Add(time.Hour)) {
		windowStart = now
		sentCount = 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		internal(w, err)
		return
	}
	code := fmt.Sprintf("%06d", n.Int64())
	verificationID := ID()
	digest, err := bcrypt.GenerateFromPassword([]byte(verificationID+":"+code), bcrypt.DefaultCost)
	if err != nil {
		internal(w, err)
		return
	}
	_, err = tx.Exec(ctx, `INSERT INTO registration_codes(email,verification_id,code_hash,expires_at,sent_at,attempts,window_started_at,send_count)
 VALUES($1,$2,$3,$4,$5,0,$6,$7) ON CONFLICT(email) DO UPDATE SET verification_id=EXCLUDED.verification_id,
 code_hash=EXCLUDED.code_hash,expires_at=EXCLUDED.expires_at,sent_at=EXCLUDED.sent_at,attempts=0,window_started_at=EXCLUDED.window_started_at,send_count=EXCLUDED.send_count`,
		email, verificationID, string(digest), now.Add(codeLifetime), now, windowStart, sentCount+1)
	if err != nil {
		internal(w, err)
		return
	}
	if err = s.Config.Mailer.SendRegistration(ctx, email, code); err != nil {
		// Do not include addresses, codes, or provider responses in logs or API errors.
		log.Printf("request=%s registration email delivery failed", w.Header().Get("X-Request-ID"))
		problem(w, 503, "EMAIL_DELIVERY_FAILED", "验证码邮件发送失败，请稍后重试")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		internal(w, err)
		return
	}
	// Cleanup runs after commit so unrelated active send transactions cannot deadlock.
	_, _ = s.DB.Exec(ctx, `DELETE FROM registration_codes WHERE window_started_at < now()-interval '1 day'; DELETE FROM email_send_limits WHERE window_started_at < now()-interval '1 day'`)
	respond(w, 200, map[string]any{"verificationId": verificationID, "expiresIn": int(codeLifetime.Seconds()), "retryAfter": int(resendDelay.Seconds())})
}

type registration struct {
	Username       string `json:"username"`
	Password       string `json:"password"`
	Email          string `json:"email"`
	VerificationID string `json:"verificationId"`
	Code           string `json:"code"`
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var input registration
	if !decode(w, r, &input) {
		return
	}
	if !usernamePattern.MatchString(input.Username) || len(input.Password) < 8 || len(input.Password) > 72 {
		problem(w, 422, "CREDENTIALS_INVALID", "用户名需为 3–24 位字母、数字或下划线，密码需为 8–72 字节")
		return
	}
	email, ok := normalizeEmail(input.Email)
	if !ok {
		problem(w, 422, "EMAIL_INVALID", "请输入有效的邮箱地址")
		return
	}
	if !codePattern.MatchString(input.Code) || len(input.VerificationID) != 32 {
		problem(w, 422, "EMAIL_CODE_INVALID", "请获取并填写 6 位邮箱验证码")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback(ctx)
	if err = emailLock(ctx, tx, email); err != nil {
		internal(w, err)
		return
	}
	var digest string
	var expires time.Time
	var attempts int
	err = tx.QueryRow(ctx, `SELECT code_hash,expires_at,attempts FROM registration_codes WHERE email=$1 AND verification_id=$2`, email, input.VerificationID).Scan(&digest, &expires, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, 422, "EMAIL_CODE_INVALID", "验证码不正确或已失效，请重新获取")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	if !time.Now().Before(expires) {
		problem(w, 422, "EMAIL_CODE_EXPIRED", "验证码已过期，请重新获取")
		return
	}
	if attempts >= 5 {
		problem(w, 422, "EMAIL_CODE_LOCKED", "验证码错误次数过多，请重新获取")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(digest), []byte(input.VerificationID+":"+input.Code)) != nil {
		if _, err = tx.Exec(ctx, `UPDATE registration_codes SET attempts=attempts+1 WHERE email=$1`, email); err != nil {
			internal(w, err)
			return
		}
		if err = tx.Commit(ctx); err != nil {
			internal(w, err)
			return
		}
		if attempts+1 >= 5 {
			problem(w, 422, "EMAIL_CODE_LOCKED", "验证码错误次数过多，请重新获取")
		} else {
			problem(w, 422, "EMAIL_CODE_INVALID", "验证码不正确，请检查邮件后重试")
		}
		return
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(input.Password), 12)
	if err != nil {
		internal(w, err)
		return
	}
	user := User{ID: ID(), Username: input.Username, EmailVerified: true}
	_, err = tx.Exec(ctx, `INSERT INTO users(id,username,password_hash,email,email_verified_at) VALUES($1,$2,$3,$4,now())`, user.ID, user.Username, string(passwordHash), email)
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			if pe.ConstraintName == "users_email_unique" {
				problem(w, 409, "EMAIL_EXISTS", "该邮箱已注册，请直接登录")
			} else {
				problem(w, 409, "USERNAME_EXISTS", "用户名已存在，请换一个用户名")
			}
		} else {
			internal(w, err)
		}
		return
	}
	if _, err = tx.Exec(ctx, `DELETE FROM registration_codes WHERE email=$1`, email); err != nil {
		internal(w, err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		internal(w, err)
		return
	}
	s.issue(w, r, user)
}
