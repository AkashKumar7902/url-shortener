// Package config loads and validates typed runtime configuration from the
// environment. Storage selection is explicit so a missing production setting
// can never silently start an ephemeral in-memory service.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// StoreBackend selects the concrete persistence implementation.
type StoreBackend string

const (
	StoreBackendMemory   StoreBackend = "memory"
	StoreBackendPostgres StoreBackend = "postgres"
)

// Config holds all runtime settings.
type Config struct {
	Addr          string // HTTP listen address
	PublicBaseURL string // trusted origin for building short_url (never the request Host)
	StoreBackend  StoreBackend
	DatabaseURL   string // required only when StoreBackend is postgres
	BlockSize     uint64 // id block size for the sequence allocator (Option A)
	CodeOffset    uint64 // starting id for the in-memory allocator (keeps codes ~6 chars)
	MaxRetries    int    // bound on the generated-code retry loop
	FeistelKey    uint64 // if non-zero, generated codes are Feistel-permuted (opaque)
}

// Load reads and validates configuration. STORE_BACKEND is deliberately
// required: memory is useful for local development, but must be an explicit
// choice because it is process-local and non-durable.
func Load() (Config, error) {
	backend, err := parseStoreBackend(os.Getenv("STORE_BACKEND"))
	if err != nil {
		return Config{}, err
	}
	databaseURL := os.Getenv("DATABASE_URL")
	switch backend {
	case StoreBackendMemory:
		if databaseURL != "" {
			return Config{}, fmt.Errorf("DATABASE_URL must be empty when STORE_BACKEND=%s", StoreBackendMemory)
		}
	case StoreBackendPostgres:
		if databaseURL == "" {
			return Config{}, fmt.Errorf("DATABASE_URL is required when STORE_BACKEND=%s", StoreBackendPostgres)
		}
	}

	c := Config{
		Addr:          getenv("HTTP_ADDR", ":8080"),
		PublicBaseURL: strings.TrimRight(getenv("PUBLIC_BASE_URL", "http://localhost:8080"), "/"),
		StoreBackend:  backend,
		DatabaseURL:   databaseURL,
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

func parseStoreBackend(raw string) (StoreBackend, error) {
	switch StoreBackend(raw) {
	case StoreBackendMemory:
		return StoreBackendMemory, nil
	case StoreBackendPostgres:
		return StoreBackendPostgres, nil
	case "":
		return "", fmt.Errorf("STORE_BACKEND is required (%s or %s)", StoreBackendMemory, StoreBackendPostgres)
	default:
		return "", fmt.Errorf("STORE_BACKEND must be %s or %s, got %q", StoreBackendMemory, StoreBackendPostgres, raw)
	}
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
