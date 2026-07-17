package config

import (
	"strings"
	"testing"
)

func TestLoadStoreBackend(t *testing.T) {
	tests := []struct {
		name        string
		backend     string
		databaseURL string
		wantBackend StoreBackend
		wantErr     string
	}{
		{
			name:        "memory is an explicit non-durable choice",
			backend:     "memory",
			wantBackend: StoreBackendMemory,
		},
		{
			name:        "postgres requires and retains its DSN",
			backend:     "postgres",
			databaseURL: "postgres://shortener@db/shortener",
			wantBackend: StoreBackendPostgres,
		},
		{
			name:    "backend is required",
			wantErr: "STORE_BACKEND is required",
		},
		{
			name:        "database URL does not imply a backend",
			databaseURL: "postgres://shortener@db/shortener",
			wantErr:     "STORE_BACKEND is required",
		},
		{
			name:    "unknown backend is rejected",
			backend: "sqlite",
			wantErr: "STORE_BACKEND must be memory or postgres",
		},
		{
			name:    "postgres without database URL is rejected",
			backend: "postgres",
			wantErr: "DATABASE_URL is required",
		},
		{
			name:        "memory rejects contradictory database URL",
			backend:     "memory",
			databaseURL: "postgres://user:super-secret@db/shortener",
			wantErr:     "DATABASE_URL must be empty",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("STORE_BACKEND", tc.backend)
			t.Setenv("DATABASE_URL", tc.databaseURL)

			got, err := Load()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Load() error = %v, want message containing %q", err, tc.wantErr)
				}
				if strings.Contains(err.Error(), "super-secret") {
					t.Fatalf("Load() error leaked DATABASE_URL credentials: %v", err)
				}
				return
			}

			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if got.StoreBackend != tc.wantBackend {
				t.Fatalf("StoreBackend = %q, want %q", got.StoreBackend, tc.wantBackend)
			}
			if got.DatabaseURL != tc.databaseURL {
				t.Fatalf("DatabaseURL = %q, want %q", got.DatabaseURL, tc.databaseURL)
			}
		})
	}
}

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"STORE_BACKEND",
		"DATABASE_URL",
		"HTTP_ADDR",
		"PUBLIC_BASE_URL",
		"BLOCK_SIZE",
		"CODE_OFFSET",
		"MAX_RETRIES",
		"FEISTEL_KEY",
	} {
		t.Setenv(key, "")
	}
}
