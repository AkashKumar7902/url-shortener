package shortener

import (
	"context"
	"crypto/rand"
	"encoding/base64"
)

// CodeGenerator produces the next candidate code for a generated link. The
// service calls it in its retry loop and is agnostic to the strategy behind it,
// so swapping sequence codes for random codes touches only wiring.
type CodeGenerator interface {
	Generate(ctx context.Context) (string, error)
}

// sequenceGenerator is Option A: an id from the allocator, encoded by the codec
// (base62, optionally Feistel-permuted for opacity).
type sequenceGenerator struct {
	alloc IDAllocator
	codec Codec
}

// NewSequenceGenerator composes an allocator and a codec into a generator.
func NewSequenceGenerator(alloc IDAllocator, codec Codec) CodeGenerator {
	return sequenceGenerator{alloc: alloc, codec: codec}
}

func (g sequenceGenerator) Generate(ctx context.Context) (string, error) {
	id, err := g.alloc.Next(ctx)
	if err != nil {
		return "", err
	}
	return g.codec.Encode(id), nil
}

// randomGenerator is the alternative strategy: opaque, non-enumerable codes from
// crypto/rand with no coordination. It satisfies the same interface, so the
// service is unchanged.
type randomGenerator struct{ bytesLen int }

// NewRandomGenerator returns a generator producing base64url codes from n random
// bytes (n=12 -> 96 bits -> 16 chars).
func NewRandomGenerator(n int) CodeGenerator {
	if n <= 0 {
		n = 12
	}
	return randomGenerator{bytesLen: n}
}

func (g randomGenerator) Generate(context.Context) (string, error) {
	b := make([]byte, g.bytesLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
