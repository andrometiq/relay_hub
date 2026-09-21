package main

import (
	"context"
	"github.com/andrometiq/relay_hub/internal/database"
	"github.com/andrometiq/relay_hub/internal/envconfig"
	"github.com/jackc/pgx/v5"
	"log"
	"os"
	"time"
)

func main() {
	if e := envconfig.Load(".env"); e != nil {
		log.Fatal(e)
	}
	if len(os.Getenv("RELAY_MCP_TOKEN")) < 32 {
		log.Fatal("RELAY_MCP_TOKEN must contain at least 32 characters")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, e := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
	if e != nil {
		log.Fatal("Database unavailable")
	}
	defer c.Close(context.Background())
	if e = database.Ready(ctx, c); e != nil {
		log.Fatal(e)
	}
}
