package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"ourtaiko.dev/fanmade/api/internal/database"
	"ourtaiko.dev/fanmade/api/internal/httpapi"
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	pool, err := database.Open(ctx, env("DATABASE_URL", "postgres://localhost/ourtaiko_fanmade?host=/tmp&sslmode=disable"))
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	if err = database.Migrate(ctx, pool, env("STORAGE_DIR", ".data/files")); err != nil {
		log.Fatal(err)
	}
	sso, err := httpapi.NewSSO(httpapi.SSOConfig{Issuer: os.Getenv("SSO_ISSUER"), ConnectAddress: os.Getenv("SSO_CONNECT_ADDRESS"), ServiceID: os.Getenv("SSO_SERVICE_ID"), ServiceKey: os.Getenv("SSO_SERVICE_KEY"), ClientID: os.Getenv("SSO_CLIENT_ID"), ClientSecret: os.Getenv("SSO_CLIENT_SECRET"), RedirectURL: os.Getenv("SSO_REDIRECT_URL"), EncryptionKey: os.Getenv("SESSION_ENCRYPTION_KEY")})
	if err != nil {
		log.Fatal(err)
	}
	if err = database.MigrateSSO(ctx, pool, func(ctx context.Context, ids []string) error {
		if os.Getenv("SSO_MIGRATION_BACKUP_CONFIRMED") != "true" {
			return fmt.Errorf("back up the database and import users into SSO before setting SSO_MIGRATION_BACKUP_CONFIRMED=true")
		}
		names, e := sso.Profiles(ctx, ids)
		if e != nil {
			return e
		}
		for _, id := range ids {
			if _, ok := names[id]; !ok {
				return fmt.Errorf("SSO is missing legacy user %s; import all users before migration", id)
			}
		}
		return nil
	}); err != nil {
		log.Fatal(err)
	}
	cancel()
	if *migrateOnly {
		log.Print("PostgreSQL migrations completed")
		return
	}
	proxies, err := httpapi.ParseTrustedProxies(os.Getenv("TRUSTED_PROXY_CIDRS"))
	if err != nil {
		log.Fatal(err)
	}
	app := httpapi.New(pool, httpapi.Config{SSO: sso, TrustedProxies: proxies, Origin: env("APP_ORIGIN", "http://127.0.0.1:5173"), Storage: env("STORAGE_DIR", ".data/files"), CookieSecure: env("COOKIE_SECURE", "false") == "true"})
	if err = app.EnsureStorage(); err != nil {
		log.Fatal(err)
	}
	cleanupCtx, stopCleanup := context.WithCancel(context.Background())
	defer stopCleanup()
	go app.RunFileCleanup(cleanupCtx)
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
