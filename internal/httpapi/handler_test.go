package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AkashKumar7902/url-shortener/internal/httpapi"
	"github.com/AkashKumar7902/url-shortener/internal/shortener"
	"github.com/AkashKumar7902/url-shortener/internal/store/memory"
)

type clock struct{}

func (clock) Now() time.Time { return time.Unix(0, 0).UTC() }

type nopLog struct{}

func (nopLog) Info(string, ...any)  {}
func (nopLog) Error(string, ...any) {}

func newServer() http.Handler {
	st := memory.New()
	gen := shortener.NewSequenceGenerator(shortener.NewCounterAllocator(1_000_000_000), shortener.Base62{})
	svc := shortener.New(st, gen, clock{}, nopLog{}, 4)
	return httpapi.New(svc, "http://localhost:8080").Routes()
}

func do(t *testing.T, h http.Handler, method, path, ctype, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestShortenAndRedirectRoundTrip(t *testing.T) {
	h := newServer()

	rec := do(t, h, "POST", "/shorten", "application/json", `{"url":"https://example.com/a"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if loc == "" {
		t.Fatal("missing Location header on create")
	}
	code := strings.TrimPrefix(loc, "http://localhost:8080/")

	// redirect
	rec = do(t, h, "GET", "/"+code, "", "")
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("want 301, got %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "https://example.com/a" {
		t.Fatalf("redirect Location = %q", got)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("expected immutable cache header, got %q", cc)
	}
}

func TestUnknownCodeIs404NoStore(t *testing.T) {
	h := newServer()
	rec := do(t, h, "GET", "/doesnotexist", "", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("want no-store, got %q", rec.Header().Get("Cache-Control"))
	}
}

func TestDuplicateURLIsIdempotent(t *testing.T) {
	h := newServer()
	first := do(t, h, "POST", "/shorten", "application/json", `{"url":"https://dup.example"}`)
	second := do(t, h, "POST", "/shorten", "application/json", `{"url":"https://dup.example"}`)
	if first.Code != http.StatusCreated {
		t.Fatalf("first want 201, got %d", first.Code)
	}
	if second.Code != http.StatusOK {
		t.Fatalf("second want 200, got %d", second.Code)
	}
}

func TestCustomAliasConflict(t *testing.T) {
	h := newServer()
	do(t, h, "POST", "/shorten", "application/json", `{"url":"https://a.example","custom_alias":"promo"}`)
	// same alias, different URL -> 409
	rec := do(t, h, "POST", "/shorten", "application/json", `{"url":"https://b.example","custom_alias":"promo"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", rec.Code)
	}
}

func TestRequestValidation(t *testing.T) {
	h := newServer()
	cases := []struct {
		name, ctype, body string
		want              int
	}{
		{"wrong ctype", "text/plain", `{"url":"https://x"}`, http.StatusUnsupportedMediaType},
		{"bad json", "application/json", `{`, http.StatusBadRequest},
		{"unknown field", "application/json", `{"url":"https://x","x":1}`, http.StatusBadRequest},
		{"invalid url", "application/json", `{"url":"ftp://x"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, "POST", "/shorten", tc.ctype, tc.body)
			if rec.Code != tc.want {
				t.Fatalf("want %d, got %d: %s", tc.want, rec.Code, rec.Body.String())
			}
		})
	}
}
