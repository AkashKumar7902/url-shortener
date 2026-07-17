// Package postgres is the production Store: a single links table where a shared
// PRIMARY KEY on code and a partial unique URL-digest index enforce the two
// invariants, and a sequence backs block-allocated generated codes.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AkashKumar7902/url-shortener/internal/shortener"
)

const sequenceName = "links_code_seq"

// All instances use the same transaction-scoped advisory lock while applying
// the embedded bootstrap migration. It prevents concurrent startups from
// racing on ALTER TABLE / index replacement.
const schemaMigrationLockID int64 = 7_902_025_071_701

// schemaStatements bootstrap a new database and upgrade the original schema.
// PostgreSQL computes a stored SHA-256 for every URL, so old and new binaries
// cannot omit or forge it. The fixed-width digest keeps the unique B-tree entry
// small even when the URL itself is near the accepted 8 KiB limit.
var schemaStatements = []string{
	`CREATE SEQUENCE IF NOT EXISTS links_code_seq START 1000000000`,
	`CREATE TABLE IF NOT EXISTS links (
    code       TEXT        PRIMARY KEY,
    url        TEXT        NOT NULL,
    url_sha256 BYTEA       GENERATED ALWAYS AS (sha256(url::bytea)) STORED,
    kind       TEXT        NOT NULL CHECK (kind IN ('generated','custom')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`,
	`ALTER TABLE links
    ADD COLUMN IF NOT EXISTS url_sha256 BYTEA
    GENERATED ALWAYS AS (sha256(url::bytea)) STORED`,
	`ALTER TABLE links ALTER COLUMN url_sha256 SET NOT NULL`,
	`DO $validation$
BEGIN
    IF NOT EXISTS (
        SELECT 1
          FROM pg_attribute
         WHERE attrelid = 'links'::regclass
           AND attname = 'url_sha256'
           AND attgenerated = 's'
           AND attnotnull
    ) THEN
        RAISE EXCEPTION 'links.url_sha256 must be a stored generated column';
    END IF;
END
$validation$`,
	`DROP INDEX IF EXISTS uq_generated_url_sha256_migration`,
	// Keep the original index name after replacing its definition. An older
	// binary in a rolling deployment then sees the name and does not recreate
	// the unsafe full-URL B-tree index. The replacement is built first, so the
	// uniqueness invariant is never removed if index creation fails.
	`DO $migration$
DECLARE
    index_definition TEXT;
BEGIN
    SELECT pg_get_indexdef(to_regclass('uq_generated_url'))
      INTO index_definition;

    IF index_definition IS NULL THEN
        CREATE UNIQUE INDEX uq_generated_url
            ON links (url_sha256) WHERE kind = 'generated';
    ELSIF position('CREATE UNIQUE INDEX' IN index_definition) = 0
       OR position('(url_sha256)' IN index_definition) = 0
       OR position('WHERE (kind = ''generated''::text)' IN index_definition) = 0 THEN
        CREATE UNIQUE INDEX uq_generated_url_sha256_migration
            ON links (url_sha256) WHERE kind = 'generated';
        DROP INDEX uq_generated_url;
        ALTER INDEX uq_generated_url_sha256_migration
            RENAME TO uq_generated_url;
    END IF;
END
$migration$`,
}

// Store implements shortener.Store over PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
}

// New connects, verifies connectivity, and transactionally applies the
// idempotent bootstrap migration.
func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: migrate: %w", err)
	}
	return &Store{pool: pool}, nil
}

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, schemaMigrationLockID); err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	for i, statement := range schemaStatements {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return fmt.Errorf("statement %d: %w", i+1, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// Insert uses ON CONFLICT DO NOTHING, which catches BOTH the code PRIMARY KEY
// and the partial URL-digest index. RowsAffected reports whether a row was
// written; PostgreSQL computes the digest stored in url_sha256.
func (s *Store) Insert(ctx context.Context, l shortener.Link) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO links (code, url, kind, created_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT DO NOTHING`,
		l.Code, l.URL, string(l.Kind), l.CreatedAt)
	if err != nil {
		return false, fmt.Errorf("postgres: insert: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) ByCode(ctx context.Context, code string) (shortener.Link, error) {
	return s.queryOne(ctx,
		`SELECT code, url, kind, created_at FROM links WHERE code = $1`, code)
}

func (s *Store) GeneratedByURL(ctx context.Context, url string) (shortener.Link, error) {
	l, err := s.queryOne(ctx,
		`SELECT code, url, kind, created_at FROM links
		 WHERE url_sha256 = sha256($1::text::bytea) AND kind = 'generated'`, url)
	if err != nil {
		return shortener.Link{}, err
	}
	// The unique digest index is the compact lookup key; exact URL equality is
	// still the public identity contract. A theoretical SHA-256 collision must
	// fail closed rather than return another URL's mapping.
	if l.URL != url {
		return shortener.Link{}, errors.New("postgres: URL SHA-256 collision")
	}
	return l, nil
}

func (s *Store) queryOne(ctx context.Context, sql string, arg any) (shortener.Link, error) {
	var l shortener.Link
	var kind string
	err := s.pool.QueryRow(ctx, sql, arg).Scan(&l.Code, &l.URL, &kind, &l.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return shortener.Link{}, shortener.ErrNotFound
	}
	if err != nil {
		return shortener.Link{}, fmt.Errorf("postgres: query: %w", err)
	}
	l.Kind = shortener.Kind(kind)
	return l, nil
}

// NextIDBlock fetches up to n sequence values in one round trip for the block
// allocator. nextval is atomic, so blocks are disjoint across all callers.
func (s *Store) NextIDBlock(ctx context.Context, n uint64) ([]uint64, error) {
	rows, err := s.pool.Query(ctx,
		fmt.Sprintf(`SELECT nextval('%s') FROM generate_series(1, $1)`, sequenceName), n)
	if err != nil {
		return nil, fmt.Errorf("postgres: nextval block: %w", err)
	}
	defer rows.Close()

	ids := make([]uint64, 0, n)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("postgres: scan id: %w", err)
		}
		ids = append(ids, uint64(id))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: id rows: %w", err)
	}
	return ids, nil
}

// Close releases the pool.
func (s *Store) Close() error {
	s.pool.Close()
	return nil
}
