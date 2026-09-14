// Package mailer sends registration codes over authenticated, verified STARTTLS.
package mailer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

type Config struct{ Host, Port, Username, Password, FromAddress, FromName string }
type SMTP struct {
	config Config
	roots  *x509.CertPool
}

func New(cfg Config) (*SMTP, error) {
	port, err := strconv.Atoi(cfg.Port)
	from, addressErr := mail.ParseAddress(cfg.FromAddress)
	if cfg.Host == "" || strings.ContainsAny(cfg.Host, "\r\n /:") || err != nil || port < 1 || port > 65535 ||
		cfg.Username == "" || cfg.Password == "" || addressErr != nil || from.Address != cfg.FromAddress || strings.ContainsAny(cfg.FromName, "\r\n") {
		return nil, errors.New("SMTP configuration is incomplete or invalid")
	}
	return &SMTP{config: cfg}, nil
}

// SendRegistration never sends credentials without TLS or skips certificate checks.
func (s *SMTP) SendRegistration(ctx context.Context, to, code string) error {
	recipient, err := mail.ParseAddress(to)
	if err != nil || recipient.Address != to || strings.ContainsAny(to, "\r\n") {
		return errors.New("invalid recipient")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cfg := s.config
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(cfg.Host, cfg.Port))
	if err != nil {
		return fmt.Errorf("SMTP connection failed: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err = conn.SetDeadline(deadline); err != nil {
		return err
	}
	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return errors.New("SMTP greeting failed")
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); !ok {
		return errors.New("SMTP server requires STARTTLS support")
	}
	if err = client.StartTLS(&tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12, RootCAs: s.roots}); err != nil {
		return fmt.Errorf("SMTP TLS failed: %w", err)
	}
	if err = client.Auth(&loginAuth{host: cfg.Host, username: cfg.Username, password: cfg.Password}); err != nil {
		return errors.New("SMTP LOGIN authentication failed")
	}
	if err = client.Mail(cfg.FromAddress); err != nil {
		return errors.New("SMTP sender rejected")
	}
	if err = client.Rcpt(to); err != nil {
		return errors.New("SMTP recipient rejected")
	}
	writer, err := client.Data()
	if err != nil {
		return errors.New("SMTP DATA rejected")
	}
	if _, err = writer.Write(message(cfg, to, code)); err != nil {
		return errors.New("SMTP message write failed")
	}
	if err = writer.Close(); err != nil {
		return errors.New("SMTP message was not accepted")
	}
	// DATA acceptance is the delivery handoff. A failed QUIT must not invalidate its code.
	_ = client.Quit()
	return nil
}

func message(cfg Config, to, code string) []byte {
	var body bytes.Buffer
	q := quotedprintable.NewWriter(&body)
	fmt.Fprintf(q, "你的 OurTaiko 注册验证码是：%s\r\n\r\n验证码 10 分钟内有效，仅能使用一次。请勿将验证码分享给他人。\r\n如果你没有申请注册，请忽略此邮件。\r\n", code)
	q.Close()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		panic(err)
	}
	domain := strings.SplitN(cfg.FromAddress, "@", 2)[1]
	headers := []string{
		"From: " + (&mail.Address{Name: cfg.FromName, Address: cfg.FromAddress}).String(),
		"To: " + (&mail.Address{Address: to}).String(),
		"Subject: " + mime.QEncoding.Encode("UTF-8", "OurTaiko 注册验证码"),
		"Date: " + time.Now().Format(time.RFC1123Z),
		"Message-ID: <" + hex.EncodeToString(nonce[:]) + "@" + domain + ">",
		"MIME-Version: 1.0", "Content-Type: text/plain; charset=UTF-8", "Content-Transfer-Encoding: quoted-printable",
	}
	return []byte(strings.Join(headers, "\r\n") + "\r\n\r\n" + body.String())
}

type loginAuth struct {
	host, username, password string
	step                     int
}

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS || server.Name != a.host {
		return "", nil, errors.New("SMTP authentication requires verified TLS")
	}
	for _, mechanism := range server.Auth {
		if strings.EqualFold(mechanism, "LOGIN") {
			a.step = 0
			return "LOGIN", nil, nil
		}
	}
	return "", nil, errors.New("SMTP LOGIN is not supported")
}
func (a *loginAuth) Next(_ []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	a.step++
	switch a.step {
	case 1:
		return []byte(a.username), nil
	case 2:
		return []byte(a.password), nil
	default:
		return nil, errors.New("unexpected SMTP LOGIN challenge")
	}
}
