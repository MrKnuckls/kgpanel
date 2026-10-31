package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"kgpanel/wings/internal/api"
	"kgpanel/wings/internal/config"
	"kgpanel/wings/internal/metrics"
	"kgpanel/wings/internal/sftpserv"
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

	// background resource sampler (CPU/mem/net/disk history per server).
	// cfg.Data is the container-visible data dir (/data) — du for disk usage
	// runs inside this container, so the host path (cfg.HostData) would not
	// resolve here.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg := metrics.NewRegistry(ctx, cfg.Data)
	defer reg.Stop()

	// SSH/SFTP server (per-server passwords, chrooted to each server's files).
	// The host key is generated once and persisted under the data dir.
	hostKey := filepath.Join(cfg.Data, "ssh_host_ed25519")
	var sftpSrv *sftpserv.Server
	if _, err := os.Stat(hostKey); err != nil {
		if err := genHostKey(hostKey); err != nil {
			log.Printf("sftp: host key: %v (SFTP disabled)", err)
			hostKey = ""
		}
	}
	if hostKey != "" {
		srv, serr := sftpserv.New(cfg.SFTPListen, cfg.Data, hostKey)
		if serr != nil {
			log.Printf("sftp: %v (SFTP disabled)", serr)
		} else if err := srv.Listen(); err != nil {
			log.Printf("sftp: %v (SFTP disabled)", err)
		} else {
			sftpSrv = srv
		}
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		cancel()
		reg.Stop()
		if sftpSrv != nil {
			sftpSrv.Close()
		}
		os.Exit(0)
	}()

	if err := http.ListenAndServe(cfg.Listen, api.Handler(cfg.Token, cfg.Data, cfg.HostData, cfg.UpdateURL, reg)); err != nil {
		log.Printf("exiting: %v", err)
		os.Exit(1)
	}
}

// genHostKey writes an RSA host key PEM to path (mode 0600).
func genHostKey(path string) error {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	b, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	out := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: b})
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return err
	}
	return nil
}
