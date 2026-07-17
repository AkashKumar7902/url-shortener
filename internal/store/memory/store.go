// Package memory is an in-process Store: a map guarded by a mutex. It enforces
// the same two invariants as the SQL store and is used for tests and for
// zero-dependency local runs.
package memory

import (
	"context"
	"sync"

	"github.com/AkashKumar7902/url-shortener/internal/shortener"
)

// Store is a concurrency-safe in-memory implementation of shortener.Store.
type Store struct {
	mu       sync.RWMutex
	byCode   map[string]shortener.Link
	genByURL map[string]string // exact url -> generated code
}

// New returns an empty in-memory store.
func New() *Store {
	return &Store{
		byCode:   make(map[string]shortener.Link),
		genByURL: make(map[string]string),
	}
}

// Insert mirrors the SQL constraints: reject on code collision, and on a second
// generated code for the same URL. Never overwrites.
func (s *Store) Insert(_ context.Context, l shortener.Link) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.byCode[l.Code]; exists {
		return false, nil // code-uniqueness conflict
	}
	if l.Kind == shortener.KindGenerated {
		if _, exists := s.genByURL[l.URL]; exists {
			return false, nil // url-idempotency conflict
		}
	}
	s.byCode[l.Code] = l
	if l.Kind == shortener.KindGenerated {
		s.genByURL[l.URL] = l.Code
	}
	return true, nil
}

func (s *Store) ByCode(_ context.Context, code string) (shortener.Link, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	l, ok := s.byCode[code]
	if !ok {
		return shortener.Link{}, shortener.ErrNotFound
	}
	return l, nil
}

func (s *Store) GeneratedByURL(_ context.Context, url string) (shortener.Link, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	code, ok := s.genByURL[url]
	if !ok {
		return shortener.Link{}, shortener.ErrNotFound
	}
	return s.byCode[code], nil
}

// Close is a no-op; present to satisfy the lifecycle used by main.
func (s *Store) Close() error { return nil }
