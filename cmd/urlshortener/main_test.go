package main

import (
	"context"
	"strings"
	"testing"

	"github.com/AkashKumar7902/url-shortener/internal/config"
)

func TestBuildStorageFailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.Config
		wantErr string
	}{
		{
			name:    "unknown backend",
			cfg:     config.Config{StoreBackend: "sqlite"},
			wantErr: "unsupported store backend",
		},
		{
			name:    "postgres without database URL",
			cfg:     config.Config{StoreBackend: config.StoreBackendPostgres},
			wantErr: "requires DATABASE_URL",
		},
		{
			name: "memory with contradictory database URL",
			cfg: config.Config{
				StoreBackend: config.StoreBackendMemory,
				DatabaseURL:  "postgres://user:secret@db/shortener",
			},
			wantErr: "cannot be configured with DATABASE_URL",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, generator, closeFn, err := buildStorage(context.Background(), tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("buildStorage() error = %v, want message containing %q", err, tc.wantErr)
			}
			if store != nil || generator != nil || closeFn != nil {
				t.Fatalf("buildStorage() returned resources after validation failure")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("buildStorage() error leaked DATABASE_URL credentials: %v", err)
			}
		})
	}
}

func TestBuildStorageUsesExplicitMemoryBackend(t *testing.T) {
	cfg := config.Config{
		StoreBackend: config.StoreBackendMemory,
		CodeOffset:   1_000_000_000,
	}
	store, generator, closeFn, err := buildStorage(context.Background(), cfg)
	if err != nil {
		t.Fatalf("buildStorage() error = %v", err)
	}
	if store == nil || generator == nil || closeFn == nil {
		t.Fatal("buildStorage() returned an incomplete memory configuration")
	}
	t.Cleanup(closeFn)
	if got := storeName(cfg); got != "memory" {
		t.Fatalf("storeName() = %q, want memory", got)
	}
}
