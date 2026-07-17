// Package platform holds small adapters for side effects (time, logging) so the
// domain can depend on interfaces and tests can substitute deterministic ones.
package platform

import (
	"log/slog"
	"os"
	"time"
)

// SystemClock returns the real wall-clock time.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

// SlogLogger adapts log/slog to the shortener.Logger interface.
type SlogLogger struct{ l *slog.Logger }

// NewLogger returns a JSON structured logger writing to stderr.
func NewLogger() SlogLogger {
	return SlogLogger{l: slog.New(slog.NewJSONHandler(os.Stderr, nil))}
}

func (s SlogLogger) Info(msg string, kv ...any)  { s.l.Info(msg, kv...) }
func (s SlogLogger) Error(msg string, kv ...any) { s.l.Error(msg, kv...) }
