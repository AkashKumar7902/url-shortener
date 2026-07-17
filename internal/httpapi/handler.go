// Package httpapi is the transport layer: routing, strict JSON decoding, and the
// domain-error to HTTP-status mapping. It holds no business logic.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/AkashKumar7902/url-shortener/internal/shortener"
)

const maxBodyBytes = 16 * 1024

// shortenService is the slice of the domain the handler needs.
type shortenService interface {
	Shorten(ctx context.Context, rawURL, alias string) (shortener.Result, error)
	Resolve(ctx context.Context, code string) (string, error)
}

// Handler wires HTTP to the service.
type Handler struct {
	svc     shortenService
	baseURL string
}

// New returns a Handler that builds short URLs from baseURL (a trusted origin).
func New(svc shortenService, baseURL string) *Handler {
	return &Handler{svc: svc, baseURL: baseURL}
}

// Routes registers method-specific routes. Because "POST /shorten" is more
// specific than "GET /{code}", the code "shorten" is still resolvable via GET.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /shorten", h.shorten)
	mux.HandleFunc("GET /{code}", h.resolve)
	return mux
}

type shortenRequest struct {
	URL         string `json:"url"`
	CustomAlias string `json:"custom_alias"`
}

type shortenResponse struct {
	Code        string `json:"code"`
	ShortURL    string `json:"short_url"`
	OriginalURL string `json:"original_url"`
	Created     bool   `json:"created"`
}

func (h *Handler) shorten(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); !isJSON(ct) {
		writeError(w, errUnsupported)
		return
	}
	req, err := decodeStrict[shortenRequest](w, r)
	if err != nil {
		writeError(w, err)
		return
	}

	res, err := h.svc.Shorten(r.Context(), req.URL, req.CustomAlias)
	if err != nil {
		writeError(w, err)
		return
	}

	shortURL := h.baseURL + "/" + res.Link.Code
	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
		w.Header().Set("Location", shortURL)
	}
	writeJSON(w, status, shortenResponse{
		Code:        res.Link.Code,
		ShortURL:    shortURL,
		OriginalURL: res.Link.URL,
		Created:     res.Created,
	})
}

func (h *Handler) resolve(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	url, err := h.svc.Resolve(r.Context(), code)
	if err != nil {
		if errors.Is(err, shortener.ErrNotFound) {
			// A missing code may be created later; do not negatively cache it.
			w.Header().Set("Cache-Control", "no-store")
		}
		writeError(w, err)
		return
	}
	// Mappings are immutable, so the redirect is safe to cache forever.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.Redirect(w, r, url, http.StatusMovedPermanently)
}

// decodeStrict enforces the 16 KiB cap, rejects unknown fields, and requires
// exactly one JSON value.
func decodeStrict[T any](w http.ResponseWriter, r *http.Request) (T, error) {
	var v T
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&v); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return v, errBodyTooLarge
		}
		return v, errBadJSON
	}
	if dec.More() {
		return v, errBadJSON
	}
	return v, nil
}

func isJSON(contentType string) bool {
	// Accept "application/json" with optional parameters (e.g. charset).
	for i := 0; i < len(contentType); i++ {
		if contentType[i] == ';' {
			contentType = contentType[:i]
			break
		}
	}
	return trimSpace(contentType) == "application/json"
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	ae := statusFor(err)
	// The Cache-Control for a 404 is set by the caller before this point.
	type errBody struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	var body errBody
	body.Error.Code = ae.code
	body.Error.Message = ae.message
	writeJSON(w, ae.status, body)
}
