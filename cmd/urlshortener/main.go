// Command urlshortener is the composition root: it loads config, wires the
// concrete store/generator/service/transport, and runs the HTTP server with
// graceful shutdown. It is the only place concrete types meet.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/AkashKumar7902/url-shortener/internal/config"
	"github.com/AkashKumar7902/url-shortener/internal/httpapi"
	"github.com/AkashKumar7902/url-shortener/internal/platform"
	"github.com/AkashKumar7902/url-shortener/internal/shortener"
	"github.com/AkashKumar7902/url-shortener/internal/store/memory"
	"github.com/AkashKumar7902/url-shortener/internal/store/postgres"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := platform.NewLogger()

	ctx := context.Background()
	store, gen, closeFn, err := buildStorage(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeFn()

	svc := shortener.New(store, gen, platform.SystemClock{}, logger, cfg.MaxRetries)
	handler := httpapi.New(svc, cfg.PublicBaseURL)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Run and wait for a termination signal, then shut down gracefully.
	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.Addr, "public_base_url", cfg.PublicBaseURL,
			"store", storeName(cfg))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		return err
	case <-stop:
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// buildStorage selects the store and generation strategy from config. With a
// DATABASE_URL it uses PostgreSQL + block-allocated sequence codes (Option A);
// otherwise the in-memory store + an in-process counter, so a fresh clone runs
// with no dependencies.
func buildStorage(ctx context.Context, cfg config.Config) (shortener.Store, shortener.CodeGenerator, func(), error) {
	codec := buildCodec(cfg)

	if cfg.DatabaseURL != "" {
		pg, err := postgres.New(ctx, cfg.DatabaseURL)
		if err != nil {
			return nil, nil, nil, err
		}
		alloc := shortener.NewBlockAllocator(cfg.BlockSize, pg.NextIDBlock)
		gen := shortener.NewSequenceGenerator(alloc, codec)
		return pg, gen, func() { _ = pg.Close() }, nil
	}

	mem := memory.New()
	alloc := shortener.NewCounterAllocator(cfg.CodeOffset)
	gen := shortener.NewSequenceGenerator(alloc, codec)
	return mem, gen, func() { _ = mem.Close() }, nil
}

func buildCodec(cfg config.Config) shortener.Codec {
	base := shortener.Base62{}
	if cfg.FeistelKey != 0 {
		// 48-bit domain: opaque, still collision-free, ~9-char codes.
		return shortener.NewFeistel(base, cfg.FeistelKey, 24)
	}
	return base
}

func storeName(cfg config.Config) string {
	if cfg.DatabaseURL != "" {
		return "postgres"
	}
	return "memory"
}
