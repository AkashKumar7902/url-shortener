package shortener

import (
	"encoding/base64"
	"fmt"
	"io"
)

const DefaultEntropyBytes = 12

// RandomGenerator produces unpadded base64url codes. With the default 12
// bytes, each candidate has 96 bits of entropy and is exactly 16 characters.
// Store uniqueness plus bounded retries, rather than probability alone,
// prevents a candidate collision from overwriting an existing link.
type RandomGenerator struct {
	reader       io.Reader
	entropyBytes int
}

func NewRandomGenerator(reader io.Reader, entropyBytes int) (*RandomGenerator, error) {
	if reader == nil {
		return nil, fmt.Errorf("reader is required")
	}
	if entropyBytes < 1 {
		return nil, fmt.Errorf("entropy bytes must be positive")
	}

	return &RandomGenerator{reader: reader, entropyBytes: entropyBytes}, nil
}

func (g *RandomGenerator) Generate() (string, error) {
	b := make([]byte, g.entropyBytes)
	if _, err := io.ReadFull(g.reader, b); err != nil {
		return "", fmt.Errorf("read entropy: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(b), nil
}
