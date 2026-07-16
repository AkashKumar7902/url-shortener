package shortener

import (
	"context"
	"errors"
	"time"
)

// Kind records how a short code was chosen. Generated links participate in
// URL deduplication; custom aliases intentionally do not.
type Kind string

const (
	KindGenerated Kind = "generated"
	KindCustom    Kind = "custom"
)

const (
	MaxURLLength   = 8 * 1024
	MaxAliasLength = 64
)

var (
	ErrNotFound                  = errors.New("link not found")
	ErrCodeAlreadyExists         = errors.New("short code already exists")
	ErrGeneratedURLAlreadyExists = errors.New("generated link for URL already exists")
	ErrInvalidURL                = errors.New("invalid URL")
	ErrInvalidAlias              = errors.New("invalid custom alias")
	ErrAliasConflict             = errors.New("custom alias is already in use")
	ErrCodeGenerationFailed      = errors.New("short-code generation failed")
	ErrCodeGenerationExhausted   = errors.New("could not allocate a unique short code")
)

// Link is an immutable code-to-destination mapping.
type Link struct {
	Code      string    `json:"code"`
	URL       string    `json:"url"`
	Kind      Kind      `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
}

// ShortenRequest distinguishes an omitted custom_alias (nil) from an explicit
// but invalid empty alias.
type ShortenRequest struct {
	URL         string
	CustomAlias *string
}

type ShortenResult struct {
	Link    Link
	Created bool
}

// Store owns the atomic uniqueness rules. Implementations must use one shared
// namespace for generated codes and custom aliases, and allow only one
// generated link for an exact URL.
type Store interface {
	GetByCode(ctx context.Context, code string) (Link, error)
	GetGeneratedByURL(ctx context.Context, rawURL string) (Link, error)
	Insert(ctx context.Context, link Link) error
}

type Generator interface {
	Generate() (string, error)
}
