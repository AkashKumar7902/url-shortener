package shortener

import (
	"net/url"
	"strings"
)

const (
	maxURLLen   = 8 * 1024
	maxAliasLen = 64
)

// validateURL accepts only well-formed absolute http/https URLs and preserves
// them byte-for-byte. It intentionally does NOT canonicalize (no host
// lowercasing, query reordering, or trailing-slash stripping): equivalent
// spellings may receive different codes, but signed URLs are never rewritten.
func validateURL(raw string) error {
	if raw == "" || len(raw) > maxURLLen {
		return ErrInvalidURL
	}
	if strings.ContainsAny(raw, " \t\r\n") || hasControlChar(raw) {
		return ErrInvalidURL
	}
	// Backslashes are treated as browser-ambiguous and rejected.
	if strings.Contains(raw, "\\") {
		return ErrInvalidURL
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ErrInvalidURL
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ErrInvalidURL
	}
	if u.Host == "" || u.Hostname() == "" {
		return ErrInvalidURL
	}
	// Reject embedded credentials (http://user:pass@host).
	if u.User != nil {
		return ErrInvalidURL
	}
	return nil
}

// validateAlias enforces the custom-alias charset and length. Aliases are
// otherwise unrestricted, so vanity names like "github" are allowed.
func validateAlias(alias string) error {
	if !validCodeSyntax(alias) {
		return ErrInvalidAlias
	}
	return nil
}

// validCodeSyntax reports whether s could be a stored code: 1..64 chars from
// [A-Za-z0-9_-]. Used on the read path to reject impossible codes before any
// datastore lookup. Every character in this set is URL-safe (RFC 3986
// unreserved), so codes never require percent-encoding.
func validCodeSyntax(s string) bool {
	if len(s) == 0 || len(s) > maxAliasLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

func hasControlChar(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
