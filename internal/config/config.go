// Package config loads typed configuration from the environment with safe
// defaults, so a fresh clone runs with no setup.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Config holds all runtime settings.
type Config struct {
	Addr          string // HTTP listen address
	PublicBaseURL string // trusted origin for building short_url (never the request Host)
	DatabaseURL   string // if empty, the in-memory store is used
	BlockSize     uint64 // id block size for the sequence allocator (Option A)
	CodeOffset    uint64 // starting id for the in-memory allocator (keeps codes ~6 chars)
	MaxRetries    int    // bound on the generated-code retry loop
	FeistelKey    uint64 // if non-zero, generated codes are Feistel-permuted (opaque)
}

// Load reads configuration and validates PublicBaseURL.
func Load() (Config, error) {
	c := Config{
		Addr:          getenv("HTTP_ADDR", ":8080"),
		PublicBaseURL: strings.TrimRight(getenv("PUBLIC_BASE_URL", "http://localhost:8080"), "/"),
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		BlockSize:     getenvUint("BLOCK_SIZE", 100),
		CodeOffset:    getenvUint("CODE_OFFSET", 1_000_000_000),
		MaxRetries:    int(getenvUint("MAX_RETRIES", 4)),
		FeistelKey:    getenvUint("FEISTEL_KEY", 0),
	}
	u, err := url.Parse(c.PublicBaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return Config{}, fmt.Errorf("PUBLIC_BASE_URL must be a bare absolute http(s) origin, got %q", c.PublicBaseURL)
	}
	return c, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvUint(key string, def uint64) uint64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}
