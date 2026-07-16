package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/meakash7902/url-shortener/internal/httpapi"
	"github.com/meakash7902/url-shortener/internal/shortener"
	filestore "github.com/meakash7902/url-shortener/internal/store/file"
)

func TestShortenRedirectRoundTrip(t *testing.T) {
	handler := newTestHandler(t)
	target := "https://example.com/path?q=signed%2Fvalue#section"

	created := performJSONRequest(t, handler, http.MethodPost, "/shorten", `{"url":"`+target+`"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("POST status = %d; want 201; body=%s", created.Code, created.Body.String())
	}
	var response shortenResponse
	decodeResponse(t, created, &response)
	if response.Code == "" || response.ShortURL != "https://sho.rt/"+response.Code {
		t.Fatalf("unexpected shorten response: %+v", response)
	}
	if response.OriginalURL != target || !response.Created {
		t.Fatalf("unexpected shorten response: %+v", response)
	}
	if location := created.Header().Get("Location"); location != response.ShortURL {
		t.Fatalf("creation Location = %q; want %q", location, response.ShortURL)
	}
	if cache := created.Header().Get("Cache-Control"); cache != "no-store" {
		t.Fatalf("creation Cache-Control = %q; want no-store", cache)
	}

	redirectRequest := httptest.NewRequest(http.MethodGet, "/"+response.Code+"?must_not_be_appended=1", nil)
	redirect := httptest.NewRecorder()
	handler.ServeHTTP(redirect, redirectRequest)
	if redirect.Code != http.StatusMovedPermanently {
		t.Fatalf("GET status = %d; want 301; body=%s", redirect.Code, redirect.Body.String())
	}
	if location := redirect.Header().Get("Location"); location != target {
		t.Fatalf("redirect Location = %q; want exact target %q", location, target)
	}
}

func TestGeneratedDuplicateReturnsExistingCode(t *testing.T) {
	handler := newTestHandler(t)
	body := `{"url":"https://example.com/repeat"}`

	first := performJSONRequest(t, handler, http.MethodPost, "/shorten", body)
	second := performJSONRequest(t, handler, http.MethodPost, "/shorten", body)
	if first.Code != http.StatusCreated || second.Code != http.StatusOK {
		t.Fatalf("duplicate statuses = (%d, %d); want (201, 200)", first.Code, second.Code)
	}
	var firstResponse, secondResponse shortenResponse
	decodeResponse(t, first, &firstResponse)
	decodeResponse(t, second, &secondResponse)
	if firstResponse.Code != secondResponse.Code || secondResponse.Created {
		t.Fatalf("duplicate responses = (%+v, %+v)", firstResponse, secondResponse)
	}
}

func TestCustomAliasHTTPPolicies(t *testing.T) {
	handler := newTestHandler(t)
	target := "https://example.com/docs"

	created := performJSONRequest(t, handler, http.MethodPost, "/shorten", `{"url":"`+target+`","custom_alias":"docs"}`)
	retry := performJSONRequest(t, handler, http.MethodPost, "/shorten", `{"url":"`+target+`","custom_alias":"docs"}`)
	additional := performJSONRequest(t, handler, http.MethodPost, "/shorten", `{"url":"`+target+`","custom_alias":"Docs"}`)
	conflict := performJSONRequest(t, handler, http.MethodPost, "/shorten", `{"url":"https://other.example","custom_alias":"docs"}`)

	if created.Code != http.StatusCreated || retry.Code != http.StatusOK || additional.Code != http.StatusCreated {
		t.Fatalf("custom alias statuses = (%d, %d, %d); want (201, 200, 201)", created.Code, retry.Code, additional.Code)
	}
	assertAPIError(t, conflict, http.StatusConflict, "alias_conflict")

	redirect := httptest.NewRecorder()
	handler.ServeHTTP(redirect, httptest.NewRequest(http.MethodGet, "/docs", nil))
	if redirect.Code != http.StatusMovedPermanently || redirect.Header().Get("Location") != target {
		t.Fatalf("alias mapping changed after conflict: status=%d location=%q", redirect.Code, redirect.Header().Get("Location"))
	}
}

func TestShortenAliasCanShareLiteralAPIPathByMethod(t *testing.T) {
	handler := newTestHandler(t)
	target := "https://example.com/api-route-alias"
	created := performJSONRequest(t, handler, http.MethodPost, "/shorten", `{"url":"`+target+`","custom_alias":"shorten"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create alias 'shorten' status = %d; body=%s", created.Code, created.Body.String())
	}

	redirect := httptest.NewRecorder()
	handler.ServeHTTP(redirect, httptest.NewRequest(http.MethodGet, "/shorten", nil))
	if redirect.Code != http.StatusMovedPermanently || redirect.Header().Get("Location") != target {
		t.Fatalf("GET /shorten = %d Location %q; want 301 to %q", redirect.Code, redirect.Header().Get("Location"), target)
	}
}

func TestUnknownCodeReturnsUncached404(t *testing.T) {
	handler := newTestHandler(t)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/unknown", nil))

	assertAPIError(t, response, http.StatusNotFound, "not_found")
	if cache := response.Header().Get("Cache-Control"); cache != "no-store" {
		t.Fatalf("Cache-Control = %q; want no-store", cache)
	}
}

func TestHTTPBoundaryErrors(t *testing.T) {
	t.Parallel()

	oversizedBody := `{"url":"https://example.com/` + strings.Repeat("a", httpapi.MaxRequestBody) + `"}`
	tests := []struct {
		name        string
		method      string
		path        string
		contentType string
		body        string
		status      int
		errorCode   string
		allow       string
	}{
		{name: "missing content type", method: http.MethodPost, path: "/shorten", body: `{"url":"https://example.com"}`, status: 415, errorCode: "unsupported_media_type"},
		{name: "wrong content type", method: http.MethodPost, path: "/shorten", contentType: "text/plain", body: `{}`, status: 415, errorCode: "unsupported_media_type"},
		{name: "malformed JSON", method: http.MethodPost, path: "/shorten", contentType: "application/json", body: `{`, status: 400, errorCode: "invalid_json"},
		{name: "unknown JSON field", method: http.MethodPost, path: "/shorten", contentType: "application/json", body: `{"url":"https://example.com","extra":true}`, status: 400, errorCode: "invalid_json"},
		{name: "multiple JSON values", method: http.MethodPost, path: "/shorten", contentType: "application/json", body: `{"url":"https://example.com"} {}`, status: 400, errorCode: "invalid_json"},
		{name: "oversized body", method: http.MethodPost, path: "/shorten", contentType: "application/json", body: oversizedBody, status: 413, errorCode: "body_too_large"},
		{name: "invalid URL", method: http.MethodPost, path: "/shorten", contentType: "application/json; charset=utf-8", body: `{"url":"javascript:alert(1)"}`, status: 400, errorCode: "invalid_url"},
		{name: "explicit empty alias", method: http.MethodPost, path: "/shorten", contentType: "application/json", body: `{"url":"https://example.com","custom_alias":""}`, status: 400, errorCode: "invalid_alias"},
		{name: "wrong code method", method: http.MethodPut, path: "/abc", status: 405, errorCode: "method_not_allowed", allow: "GET"},
		{name: "wrong shorten method", method: http.MethodDelete, path: "/shorten", status: 405, errorCode: "method_not_allowed", allow: "GET, POST"},
		{name: "root path", method: http.MethodGet, path: "/", status: 404, errorCode: "not_found"},
		{name: "nested path", method: http.MethodGet, path: "/abc/def", status: 404, errorCode: "not_found"},
		{name: "encoded slash", method: http.MethodGet, path: "/abc%2Fdef", status: 404, errorCode: "not_found"},
		{name: "invalid code grammar", method: http.MethodGet, path: "/abc.def", status: 404, errorCode: "not_found"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler := newTestHandler(t)
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertAPIError(t, response, test.status, test.errorCode)
			if got := response.Header().Get("Allow"); got != test.allow {
				t.Fatalf("Allow = %q; want %q", got, test.allow)
			}
		})
	}
}

func TestConfiguredPublicURLIgnoresRequestHost(t *testing.T) {
	handler := newTestHandler(t)
	request := httptest.NewRequest(http.MethodPost, "http://attacker.example/shorten", strings.NewReader(`{"url":"https://example.com"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	var body shortenResponse
	decodeResponse(t, response, &body)
	if !strings.HasPrefix(body.ShortURL, "https://sho.rt/") {
		t.Fatalf("short URL %q was derived from untrusted request host", body.ShortURL)
	}
}

func TestInternalErrorsAreGeneric(t *testing.T) {
	secret := errors.New("database /secret/path failed")
	service := stubService{
		shorten: func(context.Context, shortener.ShortenRequest) (shortener.ShortenResult, error) {
			return shortener.ShortenResult{}, secret
		},
		resolve: func(context.Context, string) (shortener.Link, error) {
			return shortener.Link{}, secret
		},
	}
	handler, err := httpapi.New(service, "https://sho.rt", discardLogger())
	if err != nil {
		t.Fatalf("httpapi.New() error = %v", err)
	}

	post := performJSONRequest(t, handler, http.MethodPost, "/shorten", `{"url":"https://example.com"}`)
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/code", nil))
	for _, response := range []*httptest.ResponseRecorder{post, get} {
		assertAPIError(t, response, http.StatusInternalServerError, "internal_error")
		if strings.Contains(response.Body.String(), "secret") {
			t.Fatalf("response leaked internal error: %s", response.Body.String())
		}
	}
}

func TestPublicBaseURLValidation(t *testing.T) {
	t.Parallel()

	service := stubService{}
	for _, invalid := range []string{
		"",
		"localhost:8080",
		"ftp://sho.rt",
		"https://user:pass@sho.rt",
		"https://sho.rt/prefix",
		"https://sho.rt?query=1",
		"https://sho.rt#fragment",
	} {
		if _, err := httpapi.New(service, invalid, discardLogger()); err == nil {
			t.Errorf("httpapi.New(..., %q) returned nil error", invalid)
		}
	}
	if _, err := httpapi.New(service, "https://sho.rt/", discardLogger()); err != nil {
		t.Fatalf("httpapi.New() rejected trailing slash: %v", err)
	}
}

type shortenResponse struct {
	Code        string `json:"code"`
	ShortURL    string `json:"short_url"`
	OriginalURL string `json:"original_url"`
	Created     bool   `json:"created"`
}

type errorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func newTestHandler(t *testing.T) *httpapi.Handler {
	t.Helper()
	store, err := filestore.Open(filepath.Join(t.TempDir(), "links.json"))
	if err != nil {
		t.Fatalf("file.Open() error = %v", err)
	}
	service := shortener.NewService(store, &testGenerator{})
	handler, err := httpapi.New(service, "https://sho.rt", discardLogger())
	if err != nil {
		t.Fatalf("httpapi.New() error = %v", err)
	}
	return handler
}

func performJSONRequest(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, destination any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), destination); err != nil {
		t.Fatalf("decode response body %q: %v", response.Body.String(), err)
	}
}

func assertAPIError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d; want %d; body=%s", response.Code, status, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("Content-Type = %q; want application/json", contentType)
	}
	var body errorEnvelope
	decodeResponse(t, response, &body)
	if body.Error.Code != code || body.Error.Message == "" {
		t.Fatalf("error response = %+v; want code %q and nonempty message", body, code)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type testGenerator struct {
	value atomic.Uint64
}

func (g *testGenerator) Generate() (string, error) {
	return fmt.Sprintf("generated_%d", g.value.Add(1)), nil
}

type stubService struct {
	shorten func(context.Context, shortener.ShortenRequest) (shortener.ShortenResult, error)
	resolve func(context.Context, string) (shortener.Link, error)
}

func (s stubService) Shorten(ctx context.Context, request shortener.ShortenRequest) (shortener.ShortenResult, error) {
	if s.shorten == nil {
		return shortener.ShortenResult{}, errors.New("Shorten not configured")
	}
	return s.shorten(ctx, request)
}

func (s stubService) Resolve(ctx context.Context, code string) (shortener.Link, error) {
	if s.resolve == nil {
		return shortener.Link{}, shortener.ErrNotFound
	}
	return s.resolve(ctx, code)
}
