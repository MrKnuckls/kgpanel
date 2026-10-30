// Package db manages the optional per-node MariaDB used by games that need a
// database (FiveM, MySQL-backed servers, etc.). It runs ONE shared MariaDB
// container per node (name: kggames-db) on a private docker network
// (name: kggames-net) that only game containers join — it is never exposed to
// the host network or the internet, so DB credentials never leave the box.
//
// Each server gets its own database + user (kg_<serverid> / kgu_<serverid>)
// with a generated password, delivered to the container as KG_* env vars. The
// root secret lives in the wings data dir (db-root.txt) and .env of the
// mariadb container, so recreation of the DB container keeps the same data.
package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	NetName  = "kggames-net"
	DBName   = "kggames-db"
	DBPort   = "3306"
	dbVolume = "kggames-db-data"
)

// Manager owns the node's shared database container + per-server grants.
type Manager struct {
	hostDataDir string // host-visible path for secrets (== KGWINGS_HOST_DATA or dataDir)
}

func NewManager(hostDataDir string) *Manager { return &Manager{hostDataDir: hostDataDir} }

func docker(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("docker %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// rootPassword returns the node's shared MariaDB root password, generating and
// persisting it on first use.
func (m *Manager) rootPassword() (string, error) {
	p := filepath.Join(m.hostDataDir, "db-root.txt")
	if b, err := os.ReadFile(p); err == nil && len(b) > 8 {
		return strings.TrimSpace(string(b)), nil
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	pw := hex.EncodeToString(b)
	if err := os.MkdirAll(m.hostDataDir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(pw+"\n"), 0o600); err != nil {
		return "", err
	}
	return pw, nil
}

// Ensure provisions the docker network + the shared MariaDB container, and
// waits until it answers SQL. Idempotent.
func (m *Manager) Ensure(ctx context.Context) error {
	// 1. private network (ignore "already exists")
	_, _ = docker(ctx, "network", "inspect", NetName)
	_, _ = docker(ctx, "network", "create", NetName)

	// 2. DB container — create if missing
	if _, err := docker(ctx, "inspect", DBName); err != nil {
		root, err2 := m.rootPassword()
		if err2 != nil {
			return fmt.Errorf("db: root password: %w", err2)
		}
		hostVol := filepath.Join(m.hostDataDir, "db-data") // host-visible path
		_ = hostVol
		args := []string{"run", "-d", "--name", DBName, "--restart", "unless-stopped",
			"--network", NetName, "--network-alias", "db",
			"-e", "MARIADB_ROOT_PASSWORD=" + root,
			"-v", dbVolume + ":/var/lib/mysql",
			"mariadb:11",
		}
		if _, err := docker(ctx, args...); err != nil {
			return fmt.Errorf("db: create mariadb: %w", err)
		}
	}
	// 3. wait until it answers SQL (mariadb SELECT 1 as root)
	for i := 0; i < 60; i++ {
		if _, err := m.sql(ctx, "SELECT 1"); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("db: mariadb did not become ready in time")
}

// sql runs a MariaDB command inside the DB container as root.
func (m *Manager) sql(ctx context.Context, query string) (string, error) {
	root, err := m.rootPassword()
	if err != nil {
		return "", err
	}
	return docker(ctx, "exec", DBName, "mariadb", "-uroot", "-p"+root, "-N", "-B", "-e", query)
}

// Database mirrors a server's DB grant.
type Database struct {
	Name     string
	User     string
	Password string
	Host     string
	Port     string
	Network  string
}

// Provision creates a database + user for a server and returns connection
// details. Idempotent per server.
func (m *Manager) Provision(ctx context.Context, serverID string) (*Database, error) {
	if err := m.Ensure(ctx); err != nil {
		return nil, err
	}
	safe := Sanitize(serverID)
	d := &Database{
		Name:    "kg_" + safe,
		User:    "kgu_" + safe,
		Host:    "db",
		Port:    DBPort,
		Network: NetName,
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	d.Password = hex.EncodeToString(raw)

	queries := []string{
		"CREATE DATABASE IF NOT EXISTS `" + d.Name + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci",
		"CREATE USER IF NOT EXISTS '" + d.User + "'@'%' IDENTIFIED BY '" + d.Password + "'",
		"ALTER USER '" + d.User + "'@'%' IDENTIFIED BY '" + d.Password + "'",
		"GRANT ALL PRIVILEGES ON `" + d.Name + "`.* TO '" + d.User + "'@'%'",
		"FLUSH PRIVILEGES",
	}
	for _, q := range queries {
		if _, err := m.sql(ctx, q); err != nil {
			return nil, fmt.Errorf("db: provision %s: %w", q, err)
		}
	}
	return d, nil
}

// Sanitize keeps only [a-zA-Z0-9_] (serverIDs are UUIDs; safe).
func Sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// DBEnv returns the KG_* env vars a game container should receive to use the
// provisioned database.
func (m *Manager) DBEnv(d *Database) map[string]string {
	return map[string]string{
		"KG_DB_HOST":     d.Host,
		"KG_DB_PORT":     d.Port,
		"KG_DB_NAME":     d.Name,
		"KG_DB_USER":     d.User,
		"KG_DB_PASSWORD": d.Password,
		"KG_DB_NETWORK":  d.Network,
	}
}