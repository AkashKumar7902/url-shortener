package shortener

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var codePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ValidateURL accepts absolute HTTP(S) URLs without rewriting them. Exact
// input equality is the documented deduplication key: aggressive URL
// canonicalization can change signed URLs, escaping, or query semantics.
func ValidateURL(rawURL string) (string, error) {
	switch {
	case rawURL == "":
		return "", fmt.Errorf("%w: must not be empty", ErrInvalidURL)
	case len(rawURL) > MaxURLLength:
		return "", fmt.Errorf("%w: exceeds %d bytes", ErrInvalidURL, MaxURLLength)
	case strings.TrimSpace(rawURL) != rawURL:
		return "", fmt.Errorf("%w: surrounding whitespace is not allowed", ErrInvalidURL)
	case strings.Contains(rawURL, `\`):
		return "", fmt.Errorf("%w: backslashes are not allowed", ErrInvalidURL)
	case strings.IndexFunc(rawURL, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r)
	}) >= 0:
		return "", fmt.Errorf("%w: whitespace and control characters must be percent-encoded", ErrInvalidURL)
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("%w: malformed URL", ErrInvalidURL)
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("%w: scheme must be http or https", ErrInvalidURL)
	}
	if !parsed.IsAbs() || parsed.Opaque != "" || parsed.Host == "" || parsed.Hostname() == "" {
		return "", fmt.Errorf("%w: an absolute URL with a host is required", ErrInvalidURL)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("%w: embedded credentials are not allowed", ErrInvalidURL)
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", fmt.Errorf("%w: port must be between 1 and 65535", ErrInvalidURL)
		}
	}

	return rawURL, nil
}

func ValidateAlias(alias string) error {
	switch {
	case alias == "":
		return fmt.Errorf("%w: must not be empty", ErrInvalidAlias)
	case len(alias) > MaxAliasLength:
		return fmt.Errorf("%w: exceeds %d characters", ErrInvalidAlias, MaxAliasLength)
	case !codePattern.MatchString(alias):
		return fmt.Errorf("%w: use only A-Z, a-z, 0-9, underscore, or hyphen", ErrInvalidAlias)
	default:
		return nil
	}
}

func validGeneratedCode(code string) bool {
	return code != "" && len(code) <= MaxAliasLength && codePattern.MatchString(code)
}
