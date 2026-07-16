package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/meakash7902/url-shortener/internal/httpapi"
	"github.com/meakash7902/url-shortener/internal/shortener"
	filestore "github.com/meakash7902/url-shortener/internal/store/file"
)

type config struct {
	HTTPAddress   string
	PublicBaseURL string
	DataFile      string
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("service stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg := loadConfig()

	store, err := filestore.Open(cfg.DataFile)
	if err != nil {
		return fmt.Errorf("open datastore: %w", err)
	}
	generator, err := shortener.NewRandomGenerator(rand.Reader, shortener.DefaultEntropyBytes)
	if err != nil {
		return fmt.Errorf("configure short-code generator: %w", err)
	}
	service := shortener.NewService(store, generator)
	handler, err := httpapi.New(service, cfg.PublicBaseURL, logger)
	if err != nil {
		return fmt.Errorf("configure HTTP API: %w", err)
	}

	server := &http.Server{
		Addr:              cfg.HTTPAddress,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	shutdownSignal, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	serverError := make(chan error, 1)
	go func() {
		logger.Info("starting URL shortener", "address", cfg.HTTPAddress, "public_base_url", cfg.PublicBaseURL)
		serverError <- server.ListenAndServe()
	}()

	select {
	case err := <-serverError:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP: %w", err)
		}
		return nil
	case <-shutdownSignal.Done():
		logger.Info("shutting down URL shortener")
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	if err := <-serverError; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve HTTP during shutdown: %w", err)
	}
	return nil
}

func loadConfig() config {
	return config{
		HTTPAddress:   envOrDefault("HTTP_ADDR", ":8080"),
		PublicBaseURL: envOrDefault("PUBLIC_BASE_URL", "http://localhost:8080"),
		DataFile:      envOrDefault("DATA_FILE", "./data/links.json"),
	}
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
