package shortener

import (
	"encoding/binary"
	"hash/fnv"
)

// Codec turns a numeric id into a URL-safe short code. It is a bijection over
// the id space so that distinct ids always yield distinct codes.
type Codec interface {
	Encode(id uint64) string
}

const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// Base62 encodes an id in base62 [0-9A-Za-z] — the shortest all-unreserved
// alphabet, so codes are always URL-safe and never need escaping. Codes are
// sequential (enumerable); wrap with Feistel for opacity.
type Base62 struct{}

func (Base62) Encode(id uint64) string {
	if id == 0 {
		return "0"
	}
	var buf [11]byte // ceil(64 / log2(62)) == 11
	i := len(buf)
	for id > 0 {
		i--
		buf[i] = base62Alphabet[id%62]
		id /= 62
	}
	return string(buf[i:])
}

// Feistel wraps a Codec with a keyed length-preserving permutation over a
// fixed-width domain, so sequential ids map to non-sequential (opaque) codes
// while remaining collision-free (a Feistel network is a bijection). Ids must
// be < 2^bits; the block allocator's offset keeps codes a stable length.
type Feistel struct {
	inner  Codec
	key    uint64
	bits   uint   // even; total domain width
	half   uint   // bits / 2
	mask   uint64 // (1<<half)-1
	rounds int
}

// NewFeistel returns a Feistel codec over a 2*halfBits-wide domain. halfBits=24
// gives a 48-bit domain (~2.8e14 ids), encoding to ~9 base62 chars.
func NewFeistel(inner Codec, key uint64, halfBits uint) Feistel {
	return Feistel{
		inner:  inner,
		key:    key,
		bits:   halfBits * 2,
		half:   halfBits,
		mask:   (uint64(1) << halfBits) - 1,
		rounds: 4,
	}
}

func (f Feistel) Encode(id uint64) string {
	return f.inner.Encode(f.permute(id))
}

// permute applies a balanced Feistel network. Guaranteed injective regardless
// of the round function, so no two ids collide.
func (f Feistel) permute(x uint64) uint64 {
	l := (x >> f.half) & f.mask
	r := x & f.mask
	for i := 0; i < f.rounds; i++ {
		l, r = r, l^f.round(r, uint64(i))
	}
	return (l << f.half) | r
}

func (f Feistel) round(r, i uint64) uint64 {
	h := fnv.New64a()
	var b [24]byte
	binary.LittleEndian.PutUint64(b[0:], r)
	binary.LittleEndian.PutUint64(b[8:], i)
	binary.LittleEndian.PutUint64(b[16:], f.key)
	_, _ = h.Write(b[:])
	return h.Sum64() & f.mask
}
