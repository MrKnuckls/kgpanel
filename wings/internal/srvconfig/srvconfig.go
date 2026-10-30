// Package srvconfig persists the container spec wings uses to create/start a
// server's container. The panel pushes the spec at server creation; wings
// stores it under <dataDir>/servers/<serverID>/config.json so it can (re)create
// the container independently (e.g. after a host reboot) without a panel call.
package srvconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type Spec struct {
	Image      string            `json:"image"`
	Startup    string            `json:"startup"`
	StopCmd    string            `json:"stop_command"`
	Env        map[string]string `json:"env"`
	Ports      []string          `json:"ports"`
	MemoryMB   int               `json:"memory_mb"`
	CPUPercent int               `json:"cpu_percent"`
	// Network joins the game container to a shared docker network (e.g. the
	// private kggames-net that hosts the node's MariaDB). Empty = default bridge.
	Network string `json:"network,omitempty"`
	// DB requests a per-server database on the node's shared MariaDB; wings
	// provisions DB+user and injects KG_DB_* env vars into the container.
	DB bool `json:"db,omitempty"`
}

func dir(dataDir, serverID string) string {
	return filepath.Join(dataDir, "servers", serverID)
}

func path(dataDir, serverID string) string {
	return filepath.Join(dir(dataDir, serverID), "config.json")
}

func Save(dataDir, serverID string, sp Spec) error {
	if sp.Image == "" {
		return errors.New("image required")
	}
	d := dir(dataDir, serverID)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(sp, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path(dataDir, serverID), b, 0o644)
}

// Load returns the stored spec, or an error if none present.
func Load(dataDir, serverID string) (Spec, error) {
	var sp Spec
	b, err := os.ReadFile(path(dataDir, serverID))
	if err != nil {
		return sp, err
	}
	if err := json.Unmarshal(b, &sp); err != nil {
		return sp, err
	}
	return sp, nil
}
