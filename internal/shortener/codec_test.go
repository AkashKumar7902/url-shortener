package shortener

import "testing"

func TestBase62_KnownValues(t *testing.T) {
	c := Base62{}
	cases := map[uint64]string{0: "0", 1: "1", 61: "z", 62: "10", 3843: "zz"}
	for in, want := range cases {
		if got := c.Encode(in); got != want {
			t.Errorf("Encode(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestBase62_URLSafe(t *testing.T) {
	c := Base62{}
	for id := uint64(0); id < 100000; id++ {
		if !validCodeSyntax(c.Encode(id)) {
			t.Fatalf("Encode(%d) produced a non-URL-safe code %q", id, c.Encode(id))
		}
	}
}

// TestFeistel_Bijection proves the permutation is collision-free: distinct ids
// map to distinct (and URL-safe) codes.
func TestFeistel_Bijection(t *testing.T) {
	f := NewFeistel(Base62{}, 0x9e3779b97f4a7c15, 12) // 24-bit domain for a fast full-ish sweep
	seen := make(map[string]uint64, 200000)
	for id := uint64(0); id < 200000; id++ {
		code := f.Encode(id)
		if !validCodeSyntax(code) {
			t.Fatalf("Feistel Encode(%d) not URL-safe: %q", id, code)
		}
		if prev, dup := seen[code]; dup {
			t.Fatalf("Feistel collision: ids %d and %d both map to %q", prev, id, code)
		}
		seen[code] = id
	}
}
