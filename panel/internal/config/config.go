package config

import (
	"flag"
	"os"
)

// Config holds panel runtime settings. Every value can be set via flag or env
// var (env wins), so the same binary runs under docker-compose and bare.
type Config struct {
	Listen string
	Data   string
	Secret string
}

// Load reads flags and environment, env overrides flag defaults.
func Load() Config {
	c := Config{}
	flag.StringVar(&c.Listen, "listen", ":8080", "HTTP listen address")
	flag.StringVar(&c.Data, "data", "./data", "data directory (json stores)")
	flag.StringVar(&c.Secret, "secret", "", "token signing secret (REQUIRED)")
	flag.Parse()

	if v := os.Getenv("KGPANEL_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := os.Getenv("KGPANEL_DATA"); v != "" {
		c.Data = v
	}
	if v := os.Getenv("KGPANEL_SECRET"); v != "" {
		c.Secret = v
	}
	return c
}
