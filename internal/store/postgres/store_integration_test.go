//go:build integration

// These tests exercise the real PostgreSQL store. They run only under the
// `integration` build tag and require DATABASE_URL to point at a test database.
// CI provides one via a Postgres service container.
package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AkashKumar7902/url-shortener/internal/shortener"
	"github.com/AkashKumar7902/url-shortener/internal/store/postgres"
)

func setup(t *testing.T) (*postgres.Store, context.Context) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping Postgres integration test")
	}
	ctx := context.Background()

	// Truncate for a clean slate.
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `CREATE SEQUENCE IF NOT EXISTS links_code_seq START 1000000000`); err == nil {
		_, _ = pool.Exec(ctx, `TRUNCATE TABLE links`)
	}

	st, err := postgres.New(ctx, dsn)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, ctx
}

func gen(code, url string) shortener.Link {
	return shortener.Link{Code: code, URL: url, Kind: shortener.KindGenerated, CreatedAt: time.Now().UTC()}
}

func TestInsertAndLookup(t *testing.T) {
	st, ctx := setup(t)

	ins, err := st.Insert(ctx, gen("abc", "https://example.com/a"))
	if err != nil || !ins {
		t.Fatalf("insert: inserted=%v err=%v", ins, err)
	}
	got, err := st.ByCode(ctx, "abc")
	if err != nil || got.URL != "https://example.com/a" {
		t.Fatalf("ByCode: %+v %v", got, err)
	}
	if _, err := st.ByCode(ctx, "missing"); !errors.Is(err, shortener.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestCodeUniquenessConflict(t *testing.T) {
	st, ctx := setup(t)
	if ins, _ := st.Insert(ctx, gen("dup", "https://a.example")); !ins {
		t.Fatal("first insert should succeed")
	}
	// Same code, different URL -> rejected, not overwritten.
	ins, err := st.Insert(ctx, gen("dup", "https://b.example"))
	if err != nil || ins {
		t.Fatalf("second insert should be rejected: inserted=%v err=%v", ins, err)
	}
	got, _ := st.ByCode(ctx, "dup")
	if got.URL != "https://a.example" {
		t.Fatalf("row must not be overwritten, got %q", got.URL)
	}
}

func TestGeneratedURLInvariant(t *testing.T) {
	st, ctx := setup(t)
	if ins, _ := st.Insert(ctx, gen("c1", "https://same.example")); !ins {
		t.Fatal("first generated insert should succeed")
	}
	// A second generated code for the same URL violates the partial index.
	ins, err := st.Insert(ctx, gen("c2", "https://same.example"))
	if err != nil || ins {
		t.Fatalf("second generated code for same URL should be rejected: inserted=%v err=%v", ins, err)
	}
	got, err := st.GeneratedByURL(ctx, "https://same.example")
	if err != nil || got.Code != "c1" {
		t.Fatalf("GeneratedByURL: %+v %v", got, err)
	}
}

func TestNextIDBlockDistinct(t *testing.T) {
	st, ctx := setup(t)
	ids, err := st.NextIDBlock(ctx, 100)
	if err != nil {
		t.Fatalf("NextIDBlock: %v", err)
	}
	if len(ids) != 100 {
		t.Fatalf("want 100 ids, got %d", len(ids))
	}
	seen := make(map[uint64]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("duplicate id %d in block", id)
		}
		seen[id] = true
	}
}
