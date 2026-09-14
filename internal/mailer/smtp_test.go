package mailer

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"mime/quotedprintable"
	"net"
	"net/http/httptest"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

func TestSMTP(t *testing.T) {
	for _, mode := range []string{"success", "no-starttls", "untrusted-certificate", "bad-auth"} {
		t.Run(mode, func(t *testing.T) {
			certServer := httptest.NewTLSServer(nil)
			certificate := certServer.TLS.Certificates[0]
			roots := x509.NewCertPool()
			roots.AddCert(certServer.Certificate())
			certServer.Close()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			result := make(chan string, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					result <- "accept failed"
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				tp := textproto.NewConn(conn)
				tp.PrintfLine("220 fixture")
				if _, err = tp.ReadLine(); err != nil {
					result <- "EHLO missing"
					return
				}
				if mode == "no-starttls" {
					tp.PrintfLine("250 fixture")
					result <- "no authentication"
					return
				}
				tp.PrintfLine("250-fixture\r\n250 STARTTLS")
				if line, _ := tp.ReadLine(); line != "STARTTLS" {
					result <- "STARTTLS missing"
					return
				}
				tp.PrintfLine("220 start TLS")
				secure := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
				if err = secure.Handshake(); err != nil {
					result <- "TLS rejected"
					return
				}
				tp = textproto.NewConn(secure)
				if _, err = tp.ReadLine(); err != nil {
					result <- "TLS EHLO missing"
					return
				}
				tp.PrintfLine("250-fixture\r\n250 AUTH LOGIN")
				if line, _ := tp.ReadLine(); line != "AUTH LOGIN" {
					result <- "LOGIN missing"
					return
				}
				tp.PrintfLine("334 %s", base64.StdEncoding.EncodeToString([]byte("Username:")))
				username, _ := tp.ReadLine()
				tp.PrintfLine("334 %s", base64.StdEncoding.EncodeToString([]byte("Password:")))
				password, _ := tp.ReadLine()
				if username != base64.StdEncoding.EncodeToString([]byte("fixture-user")) || password != base64.StdEncoding.EncodeToString([]byte("fixture-password")) {
					result <- "credentials mismatch"
					return
				}
				if mode == "bad-auth" {
					tp.PrintfLine("535 rejected")
					result <- "authentication rejected"
					return
				}
				tp.PrintfLine("235 accepted")
				line, _ := tp.ReadLine()
				if !strings.HasPrefix(line, "MAIL FROM:<no-reply@mail.ourtaiko.org>") {
					result <- "wrong sender"
					return
				}
				tp.PrintfLine("250 ok")
				if line, _ := tp.ReadLine(); line != "RCPT TO:<recipient@example.com>" {
					result <- "wrong recipient"
					return
				}
				tp.PrintfLine("250 ok")
				if line, _ := tp.ReadLine(); line != "DATA" {
					result <- "DATA missing"
					return
				}
				tp.PrintfLine("354 send data")
				data, err := io.ReadAll(tp.DotReader())
				if err != nil {
					result <- "read data failed"
					return
				}
				tp.PrintfLine("250 queued")
				if line, _ := tp.ReadLine(); line == "QUIT" {
					tp.PrintfLine("221 bye")
				}
				result <- string(data)
			}()
			_, port, _ := net.SplitHostPort(listener.Addr().String())
			client, err := New(Config{Host: "127.0.0.1", Port: port, Username: "fixture-user", Password: "fixture-password", FromAddress: "no-reply@mail.ourtaiko.org", FromName: "OurTaiko"})
			if err != nil {
				t.Fatal(err)
			}
			if mode != "untrusted-certificate" {
				client.roots = roots
			}
			err = client.SendRegistration(context.Background(), "recipient@example.com", "123456")
			data := <-result
			if mode != "success" {
				if err == nil {
					t.Fatal("unsafe or rejected SMTP delivery accepted", mode)
				}
				if strings.Contains(err.Error(), "fixture-password") {
					t.Fatal("error leaked password")
				}
				return
			}
			if err != nil {
				t.Fatal(err, data)
			}
			msg, err := mail.ReadMessage(strings.NewReader(data))
			if err != nil {
				t.Fatal(err, data)
			}
			from, err := mail.ParseAddress(msg.Header.Get("From"))
			if err != nil || from.Name != "OurTaiko" || from.Address != "no-reply@mail.ourtaiko.org" {
				t.Fatal("wrong From header", err)
			}
			body, err := io.ReadAll(quotedprintable.NewReader(msg.Body))
			if err != nil || !strings.Contains(string(body), "123456") || !strings.Contains(string(body), "10 分钟") {
				t.Fatal("wrong email content", err)
			}
			if msg.Header.Get("Message-ID") == "" {
				t.Fatal("missing message ID")
			}
		})
	}
}

func TestSMTPConfigurationAndLogin(t *testing.T) {
	for _, name := range []string{"OurTaiko\r\nBcc: victim@example.com"} {
		_, err := New(Config{Host: "example.com", Port: "25", Username: "user", Password: "pass", FromAddress: "sender@example.com", FromName: name})
		if err == nil {
			t.Fatal("accepted header injection")
		}
	}
	for _, info := range []*smtp.ServerInfo{{TLS: false, Name: "example.com", Auth: []string{"LOGIN"}}, {TLS: true, Name: "attacker.example.com", Auth: []string{"LOGIN"}}} {
		a := &loginAuth{host: "example.com"}
		_, _, err := a.Start(info)
		if err == nil {
			t.Fatal(fmt.Sprintf("accepted unsafe auth %+v", info))
		}
	}
}
