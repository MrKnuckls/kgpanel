package config

import (
	"flag"
	"os"
)

type Config struct {
	Listen    string
	Token     string
	Data      string
	HostData  string // host-visible data dir for docker mounts (defaults to Data)
	UpdateURL string
}

func Load() Config {
	c := Config{}
	flag.StringVar(&c.Listen, "listen", ":8081", "HTTP listen address")
	flag.StringVar(&c.Token, "token", "", "shared secret with panel (REQUIRED)")
	flag.StringVar(&c.Data, "data", "./data", "data directory for server files")
	flag.StringVar(&c.HostData, "host-data", "", "host-visible data dir for docker mounts (defaults to -data)")
	flag.StringVar(&c.UpdateURL, "update-url", "", "release feed for self-update (default: GitHub latest)")
	flag.Parse()

	if v := os.Getenv("KGWINGS_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := os.Getenv("KGWINGS_TOKEN"); v != "" {
		c.Token = v
	}
	if v := os.Getenv("KGWINGS_DATA"); v != "" {
		c.Data = v
	}
	if v := os.Getenv("KGWINGS_HOST_DATA"); v != "" {
		c.HostData = v
	}
	if v := os.Getenv("KGWINGS_UPDATE_URL"); v != "" {
		c.UpdateURL = v
	}
	if c.HostData == "" {
		c.HostData = c.Data
	}
	return c
}
