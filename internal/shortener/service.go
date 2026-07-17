package shortener

import (
	"context"
	"errors"
	"time"
)

// Store is the persistence port the domain depends on. Concrete stores
// (in-memory, PostgreSQL) implement it and own atomic uniqueness + durability;
// they hold no URL/alias policy. Defined here so the domain declares its own
// seam and there is no import cycle.
type Store interface {
	// Insert persists l atomically, returning inserted=false (nil error) on a
	// uniqueness conflict (code, or the url/generated invariant). Never
	// overwrites.
	Insert(ctx context.Context, l Link) (inserted bool, err error)
	// ByCode returns the link for code, or ErrNotFound.
	ByCode(ctx context.Context, code string) (Link, error)
	// GeneratedByURL returns the generated link for the exact url, or ErrNotFound.
	GeneratedByURL(ctx context.Context, url string) (Link, error)
}

// Clock supplies the current time; injected for deterministic tests.
type Clock interface{ Now() time.Time }

// Logger is the minimal logging surface the service needs.
type Logger interface {
	Info(msg string, kv ...any)
	Error(msg string, kv ...any)
}

// Service orchestrates both flows and owns the generated-code retry loop. It is
// the single place the write-path decision tree lives.
type Service struct {
	store      Store
	gen        CodeGenerator
	clock      Clock
	log        Logger
	maxRetries int
}

// New builds a Service. maxRetries bounds the generated-code retry loop (only
// the rare alias-overlap case consumes an attempt); <=0 defaults to 4.
func New(store Store, gen CodeGenerator, clock Clock, log Logger, maxRetries int) *Service {
	if maxRetries <= 0 {
		maxRetries = 4
	}
	return &Service{store: store, gen: gen, clock: clock, log: log, maxRetries: maxRetries}
}

// Result is the outcome of Shorten. Created distinguishes 201 from 200.
type Result struct {
	Link    Link
	Created bool
}

// Shorten validates the URL and either honours a custom alias or mints a
// generated code. See shortenCustom / shortenGenerated for the branch logic.
func (s *Service) Shorten(ctx context.Context, rawURL, alias string) (Result, error) {
	if err := validateURL(rawURL); err != nil {
		return Result{}, err
	}
	if alias != "" {
		return s.shortenCustom(ctx, rawURL, alias)
	}
	return s.shortenGenerated(ctx, rawURL)
}

// shortenCustom: optimistic insert, then reconcile only on conflict. The insert
// is the check (race-free); no wasteful write on conflict.
func (s *Service) shortenCustom(ctx context.Context, rawURL, alias string) (Result, error) {
	if err := validateAlias(alias); err != nil {
		return Result{}, err
	}
	link := Link{Code: alias, URL: rawURL, Kind: KindCustom, CreatedAt: s.clock.Now()}

	inserted, err := s.store.Insert(ctx, link)
	if err != nil {
		return Result{}, err
	}
	if inserted {
		return Result{Link: link, Created: true}, nil // 201
	}

	existing, err := s.store.ByCode(ctx, alias)
	if err != nil {
		return Result{}, err
	}
	if existing.URL == rawURL {
		return Result{Link: existing, Created: false}, nil // 200 idempotent
	}
	return Result{}, ErrAliasConflict // 409 — never overwrite
}

// shortenGenerated: dedup fast path, then a bounded retry loop. Insert conflicts
// are disambiguated by a re-read — a URL race returns the winner (200, no
// retry); an alias overlap redraws (case c).
func (s *Service) shortenGenerated(ctx context.Context, rawURL string) (Result, error) {
	if link, err := s.store.GeneratedByURL(ctx, rawURL); err == nil {
		return Result{Link: link, Created: false}, nil // 200 fast path
	} else if !errors.Is(err, ErrNotFound) {
		return Result{}, err
	}

	for attempt := 0; attempt < s.maxRetries; attempt++ {
		code, err := s.gen.Generate(ctx)
		if err != nil {
			return Result{}, err
		}
		link := Link{Code: code, URL: rawURL, Kind: KindGenerated, CreatedAt: s.clock.Now()}

		inserted, err := s.store.Insert(ctx, link)
		if err != nil {
			return Result{}, err
		}
		if inserted {
			return Result{Link: link, Created: true}, nil // case (a) -> 201
		}

		// No row inserted: distinguish URL race from code collision.
		existing, err := s.store.GeneratedByURL(ctx, rawURL)
		if err == nil {
			return Result{Link: existing, Created: false}, nil // case (b) -> 200
		}
		if !errors.Is(err, ErrNotFound) {
			return Result{}, err
		}
		// case (c): the code collided with a custom alias -> redraw and retry.
		s.log.Info("generated code collided with an existing alias; retrying",
			"code", code, "attempt", attempt)
	}
	return Result{}, ErrCodeExhausted
}

// Resolve returns the destination URL for a code, or ErrNotFound. It never
// writes: redirects are cacheable and analytics must be out-of-band.
func (s *Service) Resolve(ctx context.Context, code string) (string, error) {
	if !validCodeSyntax(code) {
		return "", ErrNotFound
	}
	link, err := s.store.ByCode(ctx, code)
	if err != nil {
		return "", err
	}
	return link.URL, nil
}
