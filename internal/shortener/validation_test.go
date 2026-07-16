package shortener_test

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/meakash7902/url-shortener/internal/shortener"
)

func TestValidateURL(t *testing.T) {
	t.Parallel()

	valid := []string{
		"https://example.com",
		"HTTP://EXAMPLE.com/Path?b=2&a=1#fragment",
		"http://127.0.0.1:8080/a",
		"https://[2001:db8::1]/resource",
		"https://example.com/a%20b?q=signed%2Fvalue",
	}
	for _, input := range valid {
		input := input
		t.Run("valid_"+input, func(t *testing.T) {
			t.Parallel()
			got, err := shortener.ValidateURL(input)
			if err != nil {
				t.Fatalf("ValidateURL(%q) returned error: %v", input, err)
			}
			if got != input {
				t.Fatalf("ValidateURL(%q) = %q; want exact input preserved", input, got)
			}
		})
	}

	invalid := []string{
		"",
		"example.com/path",
		"/relative/path",
		"ftp://example.com/file",
		"javascript:alert(1)",
		"https:///missing-host",
		"https://user:secret@example.com/path",
		" https://example.com",
		"https://example.com ",
		"https://example.com/a b",
		"https:\\example.com\\path",
		"https://example.com/%zz",
		"https://example.com:0/path",
		"https://example.com:65536/path",
		strings.Repeat("x", shortener.MaxURLLength+1),
	}
	for _, input := range invalid {
		input := input
		t.Run("invalid_"+input, func(t *testing.T) {
			t.Parallel()
			_, err := shortener.ValidateURL(input)
			if !errors.Is(err, shortener.ErrInvalidURL) {
				t.Fatalf("ValidateURL(%q) error = %v; want ErrInvalidURL", input, err)
			}
		})
	}
}

func TestValidateAlias(t *testing.T) {
	t.Parallel()

	for _, alias := range []string{"a", "A_b-9", "shorten", strings.Repeat("x", shortener.MaxAliasLength)} {
		if err := shortener.ValidateAlias(alias); err != nil {
			t.Errorf("ValidateAlias(%q) returned error: %v", alias, err)
		}
	}

	for _, alias := range []string{"", "has space", "has/slash", ".", "café", strings.Repeat("x", shortener.MaxAliasLength+1)} {
		if err := shortener.ValidateAlias(alias); !errors.Is(err, shortener.ErrInvalidAlias) {
			t.Errorf("ValidateAlias(%q) error = %v; want ErrInvalidAlias", alias, err)
		}
	}
}

func FuzzValidateURL(f *testing.F) {
	for _, seed := range []string{"https://example.com", "", "javascript:alert(1)", "https://example.com/%zz"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		validated, err := shortener.ValidateURL(input)
		if err != nil {
			return
		}
		if validated != input {
			t.Fatalf("accepted URL was rewritten: got %q, want %q", validated, input)
		}
		parsed, err := url.Parse(validated)
		if err != nil || !parsed.IsAbs() || parsed.Hostname() == "" || parsed.Opaque != "" || parsed.User != nil {
			t.Fatalf("accepted URL violates validation invariant: %q", validated)
		}
		if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
			t.Fatalf("accepted URL has unsupported scheme: %q", validated)
		}
	})
}

func FuzzValidateAlias(f *testing.F) {
	for _, seed := range []string{"docs", "A_b-9", "", "a/b"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, alias string) {
		if shortener.ValidateAlias(alias) != nil {
			return
		}
		if len(alias) == 0 || len(alias) > shortener.MaxAliasLength {
			t.Fatalf("accepted alias has invalid length: %q", alias)
		}
		for _, char := range alias {
			if !((char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') ||
				(char >= '0' && char <= '9') || char == '_' || char == '-') {
				t.Fatalf("accepted alias contains unsafe character: %q", alias)
			}
		}
	})
}
