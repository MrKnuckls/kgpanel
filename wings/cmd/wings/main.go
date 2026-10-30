package main

import (
	"log"
	"net/http"
	"os"

	"kgpanel/wings/internal/api"
	"kgpanel/wings/internal/config"
)

func main() {
	cfg := config.Load()
	if cfg.Token == "" {
		log.Fatal("KGWINGS_TOKEN / -token is required")
	}
	if err := os.MkdirAll(cfg.Data, 0o755); err != nil {
		log.Fatalf("data dir: %v", err)
	}
	log.Printf("KG Wings listening on %s (data: %s)", cfg.Listen, cfg.Data)
	if err := http.ListenAndServe(cfg.Listen, api.Handler(cfg.Token, cfg.Data)); err != nil {
		log.Printf("exiting: %v", err)
		os.Exit(1)
	}
}
