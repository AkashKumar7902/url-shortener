package shortener_test

import (
	"bytes"
	"errors"
	"regexp"
	"testing"

	"github.com/meakash7902/url-shortener/internal/shortener"
)

func TestRandomGeneratorProducesFixedLengthURLSafeCode(t *testing.T) {
	t.Parallel()

	generator, err := shortener.NewRandomGenerator(bytes.NewReader(make([]byte, shortener.DefaultEntropyBytes)), shortener.DefaultEntropyBytes)
	if err != nil {
		t.Fatalf("NewRandomGenerator() error = %v", err)
	}

	code, err := generator.Generate()
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(code) != 16 {
		t.Fatalf("code length = %d; want 16", len(code))
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{16}$`).MatchString(code) {
		t.Fatalf("code %q is not unpadded base64url", code)
	}
}

func TestRandomGeneratorPropagatesEntropyFailure(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("entropy unavailable")
	generator, err := shortener.NewRandomGenerator(failingReader{err: wantErr}, shortener.DefaultEntropyBytes)
	if err != nil {
		t.Fatalf("NewRandomGenerator() error = %v", err)
	}

	if _, err := generator.Generate(); !errors.Is(err, wantErr) {
		t.Fatalf("Generate() error = %v; want wrapped entropy error", err)
	}
}

func TestNewRandomGeneratorRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	if _, err := shortener.NewRandomGenerator(nil, shortener.DefaultEntropyBytes); err == nil {
		t.Fatal("NewRandomGenerator(nil, ...) returned nil error")
	}
	if _, err := shortener.NewRandomGenerator(bytes.NewReader(nil), 0); err == nil {
		t.Fatal("NewRandomGenerator(..., 0) returned nil error")
	}
}

type failingReader struct {
	err error
}

func (r failingReader) Read([]byte) (int, error) {
	return 0, r.err
}
