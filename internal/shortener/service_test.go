package shortener_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/AkashKumar7902/url-shortener/internal/shortener"
	"github.com/AkashKumar7902/url-shortener/internal/store/memory"
)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Unix(0, 0).UTC() }

type nopLogger struct{}

func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Error(string, ...any) {}

// scriptedGen returns codes from a queue, so tests can force specific outcomes
// (e.g. a code that collides with a pre-seeded alias).
type scriptedGen struct {
	mu    sync.Mutex
	codes []string
	i     int
}

func (g *scriptedGen) Generate(context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.i >= len(g.codes) {
		return "", errors.New("scripted generator exhausted")
	}
	c := g.codes[g.i]
	g.i++
	return c, nil
}

func newService(t *testing.T, gen shortener.CodeGenerator) (*shortener.Service, *memory.Store) {
	t.Helper()
	st := memory.New()
	return shortener.New(st, gen, fixedClock{}, nopLogger{}, 4), st
}

func TestShortenGenerated_CreateThenIdempotent(t *testing.T) {
	svc, _ := newService(t, &scriptedGen{codes: []string{"aaa", "bbb"}})
	ctx := context.Background()

	first, err := svc.Shorten(ctx, "https://example.com/x", "")
	if err != nil {
		t.Fatalf("first shorten: %v", err)
	}
	if !first.Created || first.Link.Code != "aaa" {
		t.Fatalf("want created code aaa, got %+v", first)
	}

	// Same URL again -> idempotent 200 with the SAME code (no new code minted).
	second, err := svc.Shorten(ctx, "https://example.com/x", "")
	if err != nil {
		t.Fatalf("second shorten: %v", err)
	}
	if second.Created || second.Link.Code != "aaa" {
		t.Fatalf("want idempotent code aaa, got %+v", second)
	}
}

func TestShortenGenerated_RetriesOnAliasOverlap(t *testing.T) {
	// Pre-seed a custom alias "aaa"; the generator first offers "aaa" (collides,
	// case c), then "bbb" (free). The service must skip and succeed with "bbb".
	gen := &scriptedGen{codes: []string{"aaa", "bbb"}}
	svc, st := newService(t, gen)
	ctx := context.Background()

	if _, err := svc.Shorten(ctx, "https://alias.example", "aaa"); err != nil {
		t.Fatalf("seed alias: %v", err)
	}
	res, err := svc.Shorten(ctx, "https://gen.example", "")
	if err != nil {
		t.Fatalf("generated shorten: %v", err)
	}
	if !res.Created || res.Link.Code != "bbb" {
		t.Fatalf("want retried code bbb, got %+v", res)
	}
	if got, _ := st.ByCode(ctx, "aaa"); got.URL != "https://alias.example" {
		t.Fatalf("alias must not be overwritten, got %+v", got)
	}
}

func TestShortenGenerated_Exhaustion(t *testing.T) {
	// Every candidate collides with a pre-seeded alias -> ErrCodeExhausted.
	gen := &scriptedGen{codes: []string{"c1", "c2", "c3", "c4"}}
	svc, _ := newService(t, gen)
	ctx := context.Background()
	for _, a := range []string{"c1", "c2", "c3", "c4"} {
		if _, err := svc.Shorten(ctx, "https://a.example/"+a, a); err != nil {
			t.Fatalf("seed %s: %v", a, err)
		}
	}
	if _, err := svc.Shorten(ctx, "https://gen.example", ""); !errors.Is(err, shortener.ErrCodeExhausted) {
		t.Fatalf("want ErrCodeExhausted, got %v", err)
	}
}

func TestShortenCustom_Semantics(t *testing.T) {
	svc, _ := newService(t, &scriptedGen{codes: []string{"z1"}})
	ctx := context.Background()

	// create
	r1, err := svc.Shorten(ctx, "https://example.com", "promo")
	if err != nil || !r1.Created {
		t.Fatalf("create alias: %+v %v", r1, err)
	}
	// same alias, same url -> idempotent 200
	r2, err := svc.Shorten(ctx, "https://example.com", "promo")
	if err != nil || r2.Created {
		t.Fatalf("idempotent alias: %+v %v", r2, err)
	}
	// same alias, different url -> 409, never overwritten
	if _, err := svc.Shorten(ctx, "https://evil.example", "promo"); !errors.Is(err, shortener.ErrAliasConflict) {
		t.Fatalf("want ErrAliasConflict, got %v", err)
	}
}

func TestShorten_ValidationErrors(t *testing.T) {
	svc, _ := newService(t, &scriptedGen{codes: []string{"z1"}})
	ctx := context.Background()
	cases := []struct {
		name, url, alias string
		want             error
	}{
		{"scheme", "ftp://example.com", "", shortener.ErrInvalidURL},
		{"nohost", "http://", "", shortener.ErrInvalidURL},
		{"creds", "http://u:p@example.com", "", shortener.ErrInvalidURL},
		{"space", "http://example.com/a b", "", shortener.ErrInvalidURL},
		{"badalias", "https://example.com", "no spaces", shortener.ErrInvalidAlias},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Shorten(ctx, tc.url, tc.alias); !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	svc, _ := newService(t, &scriptedGen{codes: []string{"kk"}})
	ctx := context.Background()
	if _, err := svc.Shorten(ctx, "https://example.com/dest", ""); err != nil {
		t.Fatal(err)
	}
	url, err := svc.Resolve(ctx, "kk")
	if err != nil || url != "https://example.com/dest" {
		t.Fatalf("resolve hit: %q %v", url, err)
	}
	if _, err := svc.Resolve(ctx, "missing"); !errors.Is(err, shortener.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, err := svc.Resolve(ctx, "bad code!"); !errors.Is(err, shortener.ErrNotFound) {
		t.Fatalf("invalid syntax want ErrNotFound, got %v", err)
	}
}

// TestShortenGenerated_ConcurrentSameURL exercises the URL-race (case b): many
// goroutines shorten the same URL; exactly one code must be assigned and all
// callers must receive it.
func TestShortenGenerated_ConcurrentSameURL(t *testing.T) {
	codes := make([]string, 200)
	for i := range codes {
		codes[i] = "c" + string(rune('A'+i%26)) + string(rune('a'+i/26))
	}
	st := memory.New()
	svc := shortener.New(st, &scriptedGen{codes: codes}, fixedClock{}, nopLogger{}, 8)

	const n = 50
	var wg sync.WaitGroup
	results := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := svc.Shorten(context.Background(), "https://race.example", "")
			if err != nil {
				t.Errorf("goroutine %d: %v", i, err)
				return
			}
			results[i] = r.Link.Code
		}(i)
	}
	wg.Wait()

	for i := 1; i < n; i++ {
		if results[i] != results[0] {
			t.Fatalf("concurrent same-URL produced different codes: %q vs %q", results[0], results[i])
		}
	}
}
