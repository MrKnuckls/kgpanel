package main

import (
	"log"
	"net/http"
	"os"

	"kgpanel/panel/internal/api"
	"kgpanel/panel/internal/config"
	"kgpanel/panel/internal/store"
	"kgpanel/panel/internal/version"
	"kgpanel/panel/internal/web"
)

func main() {
	cfg := config.Load()
	if cfg.Secret == "" {
		log.Fatal("KGPANEL_SECRET / -secret is required (generate: openssl rand -hex 32)")
	}
	st, err := store.New(cfg.Data)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	srv := api.New(st, cfg.Secret)
	srv.SetUpdateURL(cfg.UpdateURL)
	srv.StartScheduler()
	ui := web.UI() // embedded html/css/js
	log.Printf("KGPanel %s listening on %s (data: %s)", version.Version, cfg.Listen, cfg.Data)
	if err := http.ListenAndServe(cfg.Listen, srv.Handler(ui)); err != nil {
		log.Printf("exiting: %v", err)
		os.Exit(1)
	}
}
