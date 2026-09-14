package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"ourtaiko.dev/fanmade/api/internal/database"
	"ourtaiko.dev/fanmade/api/internal/httpapi"
	"ourtaiko.dev/fanmade/api/internal/mailer"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func main() {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Fatal("Cannot parse .env; check its syntax")
	}
	migrateOnly := flag.Bool("migrate", false, "apply migrations and exit")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	pool, err := database.Open(ctx, env("DATABASE_URL", "postgres://localhost/ourtaiko_fanmade?host=/tmp&sslmode=disable"))
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	if err = database.Migrate(ctx, pool, env("STORAGE_DIR", ".data/files")); err != nil {
		log.Fatal(err)
	}
	cancel()
	if *migrateOnly {
		log.Print("PostgreSQL migrations completed")
		return
	}
	var sender httpapi.RegistrationMailer
	if os.Getenv("SMTP_PASSWORD") != "" {
		sender, err = mailer.New(mailer.Config{Host: env("SMTP_HOST", "smtp.example.com"), Port: env("SMTP_PORT", "25"), Username: env("SMTP_USERNAME", "smtp-user@example.com"), Password: os.Getenv("SMTP_PASSWORD"), FromAddress: env("SMTP_FROM_ADDRESS", "no-reply@mail.ourtaiko.org"), FromName: env("SMTP_FROM_NAME", "OurTaiko")})
		if err != nil {
			log.Fatal(err)
		}
	} else {
		log.Print("SMTP_PASSWORD is not configured; registration email delivery is unavailable")
	}
	app := httpapi.New(pool, httpapi.Config{Mailer: sender, Origin: env("APP_ORIGIN", "http://127.0.0.1:5173"), Storage: env("STORAGE_DIR", ".data/files"), CookieSecure: env("COOKIE_SECURE", "false") == "true"})
	if err = app.EnsureStorage(); err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Addr: env("LISTEN_ADDR", "127.0.0.1:8080"), Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 180 * time.Second, WriteTimeout: 240 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 * 1024}
	go func() {
		log.Printf("OurTaiko API listening on http://%s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	srv.Shutdown(shutdown)
}
