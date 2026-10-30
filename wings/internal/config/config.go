package config

import (
	"flag"
	"os"
)

type Config struct {
	Listen string
	Token  string
	Data   string
}

func Load() Config {
	c := Config{}
	flag.StringVar(&c.Listen, "listen", ":8081", "HTTP listen address")
	flag.StringVar(&c.Token, "token", "", "shared secret with panel (REQUIRED)")
	flag.StringVar(&c.Data, "data", "./data", "data directory for server files")
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
	return c
}
