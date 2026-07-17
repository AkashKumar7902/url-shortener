package shortener

import "errors"

// Domain errors. Transport (internal/httpapi) is the only layer that maps these
// to HTTP status codes, so the domain stays protocol-agnostic.
var (
	// ErrNotFound is returned by Resolve for an unknown or syntactically
	// invalid code. It is deliberately the same sentinel the store returns.
	ErrNotFound = errors.New("not found")

	// ErrInvalidURL means the destination failed validation.
	ErrInvalidURL = errors.New("invalid url")

	// ErrInvalidAlias means a requested custom alias failed validation.
	ErrInvalidAlias = errors.New("invalid alias")

	// ErrAliasConflict means the alias already maps to a different URL and is
	// never overwritten.
	ErrAliasConflict = errors.New("alias already in use")

	// ErrCodeExhausted means the bounded generated-code retry budget was spent
	// without finding a free code. In practice this signals a broken generator
	// or a pathological namespace, not normal operation.
	ErrCodeExhausted = errors.New("code generation exhausted")
)
