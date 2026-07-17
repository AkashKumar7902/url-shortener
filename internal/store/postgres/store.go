// Package postgres is the production Store: a single links table where a shared
// PRIMARY KEY on code and a partial unique index enforce the two invariants,
// and a sequence backs block-allocated generated codes.
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

// schema is idempotent so a fresh database is usable with no external migration
// step. The constraints ARE the invariants:
//   - code PRIMARY KEY           -> one shared uniqueness namespace
//   - partial unique index (url) -> at most one generated code per exact URL
const schema = `
CREATE SEQUENCE IF NOT EXISTS links_code_seq START 1000000000;
CREATE TABLE IF NOT EXISTS links (
    code       TEXT        PRIMARY KEY,
    url        TEXT        NOT NULL,
    kind       TEXT        NOT NULL CHECK (kind IN ('generated','custom')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_generated_url ON links (url) WHERE kind = 'generated';
`

// Store implements shortener.Store over PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
}

// New connects, verifies connectivity, and applies the idempotent schema.
func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	if _, err := pool.Exec(ctx, schema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: migrate: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Insert uses ON CONFLICT DO NOTHING, which catches BOTH the code PRIMARY KEY
// and the partial url index. RowsAffected reports whether a row was written.
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
	return s.queryOne(ctx,
		`SELECT code, url, kind, created_at FROM links WHERE url = $1 AND kind = 'generated'`, url)
}

func (s *Store) queryOne(ctx context.Context, sql string, arg string) (shortener.Link, error) {
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
