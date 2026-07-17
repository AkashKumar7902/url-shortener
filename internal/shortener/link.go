// Package shortener holds the domain: URL/alias policy, idempotency, the
// generated-code retry loop, and the seams (interfaces) it depends on. It
// imports nothing from transport or a concrete datastore.
package shortener

import "time"

// Kind distinguishes an automatically generated code from a user-chosen alias.
// Both live in one shared, case-sensitive code namespace.
type Kind string

const (
	KindGenerated Kind = "generated"
	KindCustom    Kind = "custom"
)

// Link is one immutable short-code -> URL mapping.
type Link struct {
	Code      string
	URL       string
	Kind      Kind
	CreatedAt time.Time
}
