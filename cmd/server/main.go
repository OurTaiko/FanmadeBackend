package main

import (
	"context"
	"flag"
	"log"
	"os"
	"time"

	"ourtaiko.dev/fanmade/api/internal/database"
)

func main() {
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://localhost/ourtaiko_fanmade?host=/tmp&sslmode=disable"
	}
	pool, err := database.Open(ctx, url)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	if err := database.Migrate(ctx, pool); err != nil {
		log.Fatal(err)
	}
	log.Print("PostgreSQL migration completed")
}
