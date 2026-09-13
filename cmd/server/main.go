package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"ourtaiko.dev/fanmade/api/internal/database"
	"ourtaiko.dev/fanmade/api/internal/httpapi"
	"syscall"
	"time"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func main() {
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
	app := httpapi.New(pool, httpapi.Config{Origin: env("APP_ORIGIN", "http://127.0.0.1:5173"), Storage: env("STORAGE_DIR", ".data/files"), CookieSecure: env("COOKIE_SECURE", "false") == "true"})
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
