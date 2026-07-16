package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/meakash7902/url-shortener/internal/shortener"
)

const MaxRequestBody = 16 * 1024

type LinkService interface {
	Shorten(ctx context.Context, request shortener.ShortenRequest) (shortener.ShortenResult, error)
	Resolve(ctx context.Context, code string) (shortener.Link, error)
}

type Handler struct {
	service       LinkService
	publicBaseURL string
	logger        *slog.Logger
}

func New(service LinkService, publicBaseURL string, logger *slog.Logger) (*Handler, error) {
	normalizedBaseURL, err := normalizeBaseURL(publicBaseURL)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	return &Handler{
		service:       service,
		publicBaseURL: normalizedBaseURL,
		logger:        logger,
	}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if r.URL.Path == "/shorten" && r.Method == http.MethodPost {
		h.handleShorten(w, r)
		return
	}

	code, isCodePath := codeFromPath(r.URL.Path)
	if !isCodePath {
		h.writeError(w, http.StatusNotFound, "not_found", "short code not found")
		return
	}
	if r.Method != http.MethodGet {
		allowed := http.MethodGet
		if code == "shorten" {
			allowed += ", " + http.MethodPost
		}
		w.Header().Set("Allow", allowed)
		h.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}

	h.handleRedirect(w, r, code)
}

type shortenPayload struct {
	URL         string  `json:"url"`
	CustomAlias *string `json:"custom_alias,omitempty"`
}

type shortenResponse struct {
	Code        string `json:"code"`
	ShortURL    string `json:"short_url"`
	OriginalURL string `json:"original_url"`
	Created     bool   `json:"created"`
}

type errorResponse struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (h *Handler) handleShorten(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		h.writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var payload shortenPayload
	if err := decoder.Decode(&payload); err != nil {
		h.writeJSONDecodeError(w, err)
		return
	}
	if err := expectJSONEnd(decoder); err != nil {
		h.writeJSONDecodeError(w, err)
		return
	}

	result, err := h.service.Shorten(r.Context(), shortener.ShortenRequest{
		URL:         payload.URL,
		CustomAlias: payload.CustomAlias,
	})
	if err != nil {
		h.writeServiceError(w, err)
		return
	}

	shortURL := h.publicBaseURL + "/" + result.Link.Code
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
		w.Header().Set("Location", shortURL)
	}
	w.Header().Set("Cache-Control", "no-store")
	h.writeJSON(w, status, shortenResponse{
		Code:        result.Link.Code,
		ShortURL:    shortURL,
		OriginalURL: result.Link.URL,
		Created:     result.Created,
	})
}

func (h *Handler) handleRedirect(w http.ResponseWriter, r *http.Request, code string) {
	link, err := h.service.Resolve(r.Context(), code)
	if errors.Is(err, shortener.ErrNotFound) {
		h.writeError(w, http.StatusNotFound, "not_found", "short code not found")
		return
	}
	if err != nil {
		h.logger.Error("resolve short code", "error", err)
		h.writeError(w, http.StatusInternalServerError, "internal_error", "an internal error occurred")
		return
	}

	// The assignment explicitly requires 301. Links are immutable so a cached
	// permanent redirect cannot become stale after a destination update.
	http.Redirect(w, r, link.URL, http.StatusMovedPermanently)
}

func (h *Handler) writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, shortener.ErrInvalidURL):
		h.writeError(w, http.StatusBadRequest, "invalid_url", err.Error())
	case errors.Is(err, shortener.ErrInvalidAlias):
		h.writeError(w, http.StatusBadRequest, "invalid_alias", err.Error())
	case errors.Is(err, shortener.ErrAliasConflict):
		h.writeError(w, http.StatusConflict, "alias_conflict", "custom alias is already in use")
	default:
		h.logger.Error("shorten URL", "error", err)
		h.writeError(w, http.StatusInternalServerError, "internal_error", "an internal error occurred")
	}
}

func (h *Handler) writeJSONDecodeError(w http.ResponseWriter, err error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		h.writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", fmt.Sprintf("request body must not exceed %d bytes", MaxRequestBody))
		return
	}
	h.writeError(w, http.StatusBadRequest, "invalid_json", "body must be one valid JSON object with only url and optional custom_alias")
}

func (h *Handler) writeError(w http.ResponseWriter, status int, code, message string) {
	// 404 responses can be cached heuristically. Disabling error caching avoids
	// a stale negative result if that alias is created later.
	w.Header().Set("Cache-Control", "no-store")
	h.writeJSON(w, status, errorResponse{Error: apiError{Code: code, Message: message}})
}

func (h *Handler) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		h.logger.Error("encode HTTP response", "error", err)
	}
}

func codeFromPath(path string) (string, bool) {
	if len(path) < 2 || path[0] != '/' || strings.Count(path, "/") != 1 {
		return "", false
	}
	code := path[1:]
	if shortener.ValidateAlias(code) != nil {
		return "", false
	}
	return code, true
}

func normalizeBaseURL(raw string) (string, error) {
	validated, err := shortener.ValidateURL(raw)
	if err != nil {
		return "", fmt.Errorf("invalid PUBLIC_BASE_URL: %w", err)
	}
	parsed, err := url.Parse(validated)
	if err != nil {
		return "", fmt.Errorf("invalid PUBLIC_BASE_URL: %w", err)
	}
	if (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("invalid PUBLIC_BASE_URL: path, query, and fragment are not allowed")
	}
	return strings.TrimSuffix(validated, "/"), nil
}

func expectJSONEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}
