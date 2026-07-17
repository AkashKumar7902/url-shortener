//go:build integration

// These tests exercise the real PostgreSQL store. They run only under the
// `integration` build tag and require TEST_DATABASE_URL to point at a disposable
// test database. CI provides one via a Postgres service container.
package postgres_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AkashKumar7902/url-shortener/internal/shortener"
	"github.com/AkashKumar7902/url-shortener/internal/store/postgres"
)

func setup(t *testing.T) (*postgres.Store, context.Context) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Postgres integration test")
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

func TestGeneratedURLInvariantForMaxLengthURL(t *testing.T) {
	st, ctx := setup(t)
	longURL := highEntropyURL(8 * 1024)

	if ins, err := st.Insert(ctx, gen("long1", longURL)); err != nil || !ins {
		t.Fatalf("insert 8 KiB URL: inserted=%v err=%v", ins, err)
	}
	if ins, err := st.Insert(ctx, gen("long2", longURL)); err != nil || ins {
		t.Fatalf("duplicate 8 KiB URL: inserted=%v err=%v", ins, err)
	}
	got, err := st.GeneratedByURL(ctx, longURL)
	if err != nil || got.Code != "long1" || got.URL != longURL {
		t.Fatalf("GeneratedByURL(long): code=%q url-match=%v err=%v", got.Code, got.URL == longURL, err)
	}

	pool, err := pgxpool.New(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatalf("connect for digest assertion: %v", err)
	}
	defer pool.Close()
	var digestBytes int
	if err := pool.QueryRow(ctx,
		`SELECT octet_length(url_sha256) FROM links WHERE code = 'long1'`).Scan(&digestBytes); err != nil {
		t.Fatalf("read URL digest: %v", err)
	}
	if digestBytes != sha256.Size {
		t.Fatalf("url_sha256 length = %d, want %d", digestBytes, sha256.Size)
	}
}

func TestGeneratedURLInvariantConcurrent(t *testing.T) {
	st, ctx := setup(t)
	const callers = 24
	const rawURL = "https://race.example/one-url"

	type result struct {
		inserted bool
		err      error
	}
	results := make(chan result, callers)
	for i := 0; i < callers; i++ {
		go func(i int) {
			inserted, err := st.Insert(ctx, gen(fmt.Sprintf("race-%02d", i), rawURL))
			results <- result{inserted: inserted, err: err}
		}(i)
	}

	created := 0
	for i := 0; i < callers; i++ {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent insert: %v", result.err)
		}
		if result.inserted {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created rows = %d, want 1", created)
	}
	if _, err := st.GeneratedByURL(ctx, rawURL); err != nil {
		t.Fatalf("GeneratedByURL() after concurrent inserts: %v", err)
	}
}

func TestMigratesLegacyURLIndex(t *testing.T) {
	dsn := testDatabaseURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	const legacyURL = "https://example.com/caf%C3%A9?label=é"
	_, err = pool.Exec(ctx, `
DROP TABLE IF EXISTS links;
DROP SEQUENCE IF EXISTS links_code_seq;
CREATE SEQUENCE links_code_seq START 1000000000;
CREATE TABLE links (
    code       TEXT        PRIMARY KEY,
    url        TEXT        NOT NULL,
    kind       TEXT        NOT NULL CHECK (kind IN ('generated','custom')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX uq_generated_url ON links (url) WHERE kind = 'generated';
`)
	if err == nil {
		_, err = pool.Exec(ctx,
			`INSERT INTO links (code, url, kind) VALUES ('legacy', $1, 'generated')`, legacyURL)
	}
	pool.Close()
	if err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}

	st, err := postgres.New(ctx, dsn)
	if err != nil {
		t.Fatalf("migrate legacy schema: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	got, err := st.GeneratedByURL(ctx, legacyURL)
	if err != nil || got.Code != "legacy" || got.URL != legacyURL {
		t.Fatalf("migrated lookup: %+v err=%v", got, err)
	}

	verify, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect for migration assertions: %v", err)
	}
	defer verify.Close()
	var digestBytes int
	var indexDefinition string
	if err := verify.QueryRow(ctx,
		`SELECT octet_length(url_sha256) FROM links WHERE code = 'legacy'`).Scan(&digestBytes); err != nil {
		t.Fatalf("read migrated digest: %v", err)
	}
	// The replacement deliberately keeps the legacy index name. Verify that an
	// older binary's idempotent bootstrap statement cannot recreate the unsafe
	// full-URL B-tree index during a rolling deployment.
	if _, err := verify.Exec(ctx,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_generated_url ON links (url) WHERE kind = 'generated'`); err != nil {
		t.Fatalf("run legacy bootstrap after migration: %v", err)
	}
	if err := verify.QueryRow(ctx,
		`SELECT pg_get_indexdef('uq_generated_url'::regclass)`).Scan(&indexDefinition); err != nil {
		t.Fatalf("read migrated index: %v", err)
	}
	if digestBytes != sha256.Size {
		t.Fatalf("migrated digest length = %d, want %d", digestBytes, sha256.Size)
	}
	if !strings.Contains(indexDefinition, "(url_sha256)") {
		t.Fatalf("uq_generated_url was not migrated to the digest column: %s", indexDefinition)
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

func testDatabaseURL(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Postgres integration test")
	}
	return dsn
}

func highEntropyURL(length int) string {
	const prefix = "https://example.com/download?token="
	var b strings.Builder
	b.Grow(length + sha256.Size*2)
	b.WriteString(prefix)
	digest := sha256.Sum256([]byte("deterministic URL test seed"))
	for b.Len() < length {
		digest = sha256.Sum256(digest[:])
		b.WriteString(hex.EncodeToString(digest[:]))
	}
	return b.String()[:length]
}
