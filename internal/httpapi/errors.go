package httpapi

import (
	"errors"
	"net/http"

	"github.com/AkashKumar7902/url-shortener/internal/shortener"
)

// apiError carries an HTTP status plus a stable machine code and message.
type apiError struct {
	status  int
	code    string
	message string
}

func (e apiError) Error() string { return e.message }

// transport-level errors (decoding). Domain errors are mapped in statusFor.
var (
	errUnsupported  = apiError{http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json"}
	errBodyTooLarge = apiError{http.StatusRequestEntityTooLarge, "payload_too_large", "request body too large"}
	errBadJSON      = apiError{http.StatusBadRequest, "bad_request", "request body is not valid JSON"}
	errInternal     = apiError{http.StatusInternalServerError, "internal", "internal server error"}
)

// statusFor maps a domain (or api) error to the response shape. It is the single
// place the domain-to-HTTP contract lives.
func statusFor(err error) apiError {
	var ae apiError
	if errors.As(err, &ae) {
		return ae
	}
	switch {
	case errors.Is(err, shortener.ErrNotFound):
		return apiError{http.StatusNotFound, "not_found", "short code not found"}
	case errors.Is(err, shortener.ErrInvalidURL):
		return apiError{http.StatusBadRequest, "invalid_url", "the provided URL is invalid"}
	case errors.Is(err, shortener.ErrInvalidAlias):
		return apiError{http.StatusBadRequest, "invalid_alias", "custom alias must be 1-64 chars of [A-Za-z0-9_-]"}
	case errors.Is(err, shortener.ErrAliasConflict):
		return apiError{http.StatusConflict, "alias_conflict", "custom alias is already in use"}
	default:
		return errInternal
	}
}
