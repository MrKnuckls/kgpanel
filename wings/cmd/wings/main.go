package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"kgpanel/wings/internal/api"
	"kgpanel/wings/internal/config"
	"kgpanel/wings/internal/metrics"
	"kgpanel/wings/internal/version"
)

func main() {
	cfg := config.Load()
	if cfg.Token == "" {
		log.Fatal("KGWINGS_TOKEN / -token is required")
	}
	if err := os.MkdirAll(cfg.Data, 0o755); err != nil {
		log.Fatalf("data dir: %v", err)
	}
	log.Printf("KG Wings %s listening on %s (data: %s)", version.Version, cfg.Listen, cfg.Data)

	// background resource sampler (CPU/mem/net/disk history per server)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg := metrics.NewRegistry(ctx, cfg.HostData)
	defer reg.Stop()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		cancel()
		reg.Stop()
		os.Exit(0)
	}()

	if err := http.ListenAndServe(cfg.Listen, api.Handler(cfg.Token, cfg.Data, cfg.HostData, cfg.UpdateURL, reg)); err != nil {
		log.Printf("exiting: %v", err)
		os.Exit(1)
	}
}
