package shortener

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const maxCodeAttempts = 8

type Service struct {
	store     Store
	generator Generator
	now       func() time.Time
}

func NewService(store Store, generator Generator) *Service {
	return &Service{
		store:     store,
		generator: generator,
		now:       time.Now,
	}
}

// Shorten implements two deliberate identities:
//   - generated requests are idempotent by exact URL;
//   - custom requests are idempotent by alias and may create additional
//     aliases for a URL that was already shortened.
func (s *Service) Shorten(ctx context.Context, request ShortenRequest) (ShortenResult, error) {
	rawURL, err := ValidateURL(request.URL)
	if err != nil {
		return ShortenResult{}, err
	}

	if request.CustomAlias != nil {
		return s.shortenCustom(ctx, rawURL, *request.CustomAlias)
	}

	return s.shortenGenerated(ctx, rawURL)
}

func (s *Service) Resolve(ctx context.Context, code string) (Link, error) {
	return s.store.GetByCode(ctx, code)
}

func (s *Service) shortenCustom(ctx context.Context, rawURL, alias string) (ShortenResult, error) {
	if err := ValidateAlias(alias); err != nil {
		return ShortenResult{}, err
	}

	existing, err := s.store.GetByCode(ctx, alias)
	switch {
	case err == nil:
		if existing.URL == rawURL {
			return ShortenResult{Link: existing, Created: false}, nil
		}
		return ShortenResult{}, ErrAliasConflict
	case !errors.Is(err, ErrNotFound):
		return ShortenResult{}, fmt.Errorf("look up custom alias: %w", err)
	}

	link := Link{
		Code:      alias,
		URL:       rawURL,
		Kind:      KindCustom,
		CreatedAt: s.now().UTC(),
	}
	if err := s.store.Insert(ctx, link); err != nil {
		if !errors.Is(err, ErrCodeAlreadyExists) {
			return ShortenResult{}, fmt.Errorf("store custom alias: %w", err)
		}

		// The insert is authoritative. Re-read after a concurrent claimant won.
		winner, getErr := s.store.GetByCode(ctx, alias)
		if getErr != nil {
			return ShortenResult{}, fmt.Errorf("read custom-alias winner: %w", getErr)
		}
		if winner.URL == rawURL {
			return ShortenResult{Link: winner, Created: false}, nil
		}
		return ShortenResult{}, ErrAliasConflict
	}

	return ShortenResult{Link: link, Created: true}, nil
}

func (s *Service) shortenGenerated(ctx context.Context, rawURL string) (ShortenResult, error) {
	existing, err := s.store.GetGeneratedByURL(ctx, rawURL)
	switch {
	case err == nil:
		return ShortenResult{Link: existing, Created: false}, nil
	case !errors.Is(err, ErrNotFound):
		return ShortenResult{}, fmt.Errorf("look up generated link: %w", err)
	}

	for attempt := 0; attempt < maxCodeAttempts; attempt++ {
		code, err := s.generator.Generate()
		if err != nil {
			return ShortenResult{}, fmt.Errorf("%w: %v", ErrCodeGenerationFailed, err)
		}
		if !validGeneratedCode(code) {
			return ShortenResult{}, fmt.Errorf("%w: generator returned a non-URL-safe code", ErrCodeGenerationFailed)
		}

		link := Link{
			Code:      code,
			URL:       rawURL,
			Kind:      KindGenerated,
			CreatedAt: s.now().UTC(),
		}
		err = s.store.Insert(ctx, link)
		switch {
		case err == nil:
			return ShortenResult{Link: link, Created: true}, nil
		case errors.Is(err, ErrCodeAlreadyExists):
			// The candidate collided with either a generated code or custom alias.
			// A new candidate is safe because no existing row was overwritten.
			continue
		case errors.Is(err, ErrGeneratedURLAlreadyExists):
			winner, getErr := s.store.GetGeneratedByURL(ctx, rawURL)
			if getErr != nil {
				return ShortenResult{}, fmt.Errorf("read generated-link winner: %w", getErr)
			}
			return ShortenResult{Link: winner, Created: false}, nil
		default:
			return ShortenResult{}, fmt.Errorf("store generated link: %w", err)
		}
	}

	return ShortenResult{}, ErrCodeGenerationExhausted
}
