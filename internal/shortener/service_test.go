package shortener_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/meakash7902/url-shortener/internal/shortener"
	filestore "github.com/meakash7902/url-shortener/internal/store/file"
)

func TestGeneratedShorteningIsIdempotent(t *testing.T) {
	store := openTestStore(t)
	generator := &scriptedGenerator{codes: []string{"first_generated"}}
	service := shortener.NewService(store, generator)

	first, err := service.Shorten(context.Background(), shortener.ShortenRequest{URL: "https://example.com/a"})
	if err != nil {
		t.Fatalf("first Shorten() error = %v", err)
	}
	second, err := service.Shorten(context.Background(), shortener.ShortenRequest{URL: "https://example.com/a"})
	if err != nil {
		t.Fatalf("second Shorten() error = %v", err)
	}

	if !first.Created || second.Created {
		t.Fatalf("created flags = (%v, %v); want (true, false)", first.Created, second.Created)
	}
	if first.Link.Code != second.Link.Code {
		t.Fatalf("duplicate URL returned codes %q and %q", first.Link.Code, second.Link.Code)
	}
	if calls := generator.Calls(); calls != 1 {
		t.Fatalf("generator calls = %d; want 1", calls)
	}
}

func TestCustomAliasPolicies(t *testing.T) {
	store := openTestStore(t)
	service := shortener.NewService(store, staticGenerator{code: "unused"})
	ctx := context.Background()
	alias := "docs"

	first, err := service.Shorten(ctx, shortener.ShortenRequest{URL: "https://example.com/docs", CustomAlias: &alias})
	if err != nil || !first.Created {
		t.Fatalf("first custom Shorten() = %+v, %v; want created result", first, err)
	}
	retry, err := service.Shorten(ctx, shortener.ShortenRequest{URL: "https://example.com/docs", CustomAlias: &alias})
	if err != nil || retry.Created {
		t.Fatalf("custom retry = %+v, %v; want existing result", retry, err)
	}

	secondAlias := "documentation"
	additional, err := service.Shorten(ctx, shortener.ShortenRequest{URL: "https://example.com/docs", CustomAlias: &secondAlias})
	if err != nil || !additional.Created {
		t.Fatalf("additional alias = %+v, %v; want created result", additional, err)
	}

	_, err = service.Shorten(ctx, shortener.ShortenRequest{URL: "https://other.example/docs", CustomAlias: &alias})
	if !errors.Is(err, shortener.ErrAliasConflict) {
		t.Fatalf("conflicting alias error = %v; want ErrAliasConflict", err)
	}
	resolved, err := service.Resolve(ctx, alias)
	if err != nil || resolved.URL != "https://example.com/docs" {
		t.Fatalf("mapping was overwritten after conflict: link=%+v err=%v", resolved, err)
	}
}

func TestCustomAliasIsHonoredAfterURLWasGenerated(t *testing.T) {
	store := openTestStore(t)
	service := shortener.NewService(store, staticGenerator{code: "automatic_code"})
	ctx := context.Background()
	target := "https://example.com/already-generated"

	generated, err := service.Shorten(ctx, shortener.ShortenRequest{URL: target})
	if err != nil {
		t.Fatalf("generated Shorten() error = %v", err)
	}
	alias := "readable_alias"
	custom, err := service.Shorten(ctx, shortener.ShortenRequest{URL: target, CustomAlias: &alias})
	if err != nil {
		t.Fatalf("custom Shorten() error = %v", err)
	}
	if !custom.Created || custom.Link.Code != alias {
		t.Fatalf("custom result = %+v; want newly created alias %q", custom, alias)
	}
	if custom.Link.Code == generated.Link.Code {
		t.Fatalf("explicit alias was ignored in favor of generated code %q", generated.Link.Code)
	}
}

func TestCustomAliasCannotOverwriteGeneratedCode(t *testing.T) {
	store := openTestStore(t)
	service := shortener.NewService(store, staticGenerator{code: "generated_claim"})
	ctx := context.Background()

	generated, err := service.Shorten(ctx, shortener.ShortenRequest{URL: "https://existing.example"})
	if err != nil {
		t.Fatalf("generated Shorten() error = %v", err)
	}
	alias := generated.Link.Code
	_, err = service.Shorten(ctx, shortener.ShortenRequest{
		URL:         "https://attacker.example",
		CustomAlias: &alias,
	})
	if !errors.Is(err, shortener.ErrAliasConflict) {
		t.Fatalf("custom Shorten() error = %v; want ErrAliasConflict", err)
	}
	resolved, err := service.Resolve(ctx, generated.Link.Code)
	if err != nil || resolved.URL != "https://existing.example" {
		t.Fatalf("generated mapping was overwritten: link=%+v err=%v", resolved, err)
	}
}

func TestGeneratedCodeCollisionRetriesWithoutOverwrite(t *testing.T) {
	store := openTestStore(t)
	setupService := shortener.NewService(store, staticGenerator{code: "unused"})
	occupied := "occupied"
	if _, err := setupService.Shorten(context.Background(), shortener.ShortenRequest{
		URL:         "https://existing.example",
		CustomAlias: &occupied,
	}); err != nil {
		t.Fatalf("create occupied alias: %v", err)
	}

	generator := &scriptedGenerator{codes: []string{"occupied", "fresh_code"}}
	service := shortener.NewService(store, generator)
	result, err := service.Shorten(context.Background(), shortener.ShortenRequest{URL: "https://new.example"})
	if err != nil {
		t.Fatalf("Shorten() error = %v", err)
	}
	if result.Link.Code != "fresh_code" {
		t.Fatalf("code = %q; want fresh_code", result.Link.Code)
	}
	if calls := generator.Calls(); calls != 2 {
		t.Fatalf("generator calls = %d; want 2", calls)
	}

	original, err := service.Resolve(context.Background(), occupied)
	if err != nil || original.URL != "https://existing.example" {
		t.Fatalf("occupied alias was overwritten: link=%+v err=%v", original, err)
	}
}

func TestGeneratedCodeCollisionExhaustion(t *testing.T) {
	store := openTestStore(t)
	service := shortener.NewService(store, staticGenerator{code: "taken"})
	alias := "taken"
	if _, err := service.Shorten(context.Background(), shortener.ShortenRequest{
		URL:         "https://existing.example",
		CustomAlias: &alias,
	}); err != nil {
		t.Fatalf("create occupied alias: %v", err)
	}

	_, err := service.Shorten(context.Background(), shortener.ShortenRequest{URL: "https://new.example"})
	if !errors.Is(err, shortener.ErrCodeGenerationExhausted) {
		t.Fatalf("Shorten() error = %v; want ErrCodeGenerationExhausted", err)
	}
}

func TestConcurrentGeneratedRequestsConvergeOnOneCode(t *testing.T) {
	store := openTestStore(t)
	service := shortener.NewService(store, &countingGenerator{})
	const workers = 64

	start := make(chan struct{})
	results := make(chan shortener.ShortenResult, workers)
	errorsFound := make(chan error, workers)
	var waitGroup sync.WaitGroup
	for i := 0; i < workers; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			result, err := service.Shorten(context.Background(), shortener.ShortenRequest{URL: "https://example.com/concurrent"})
			if err != nil {
				errorsFound <- err
				return
			}
			results <- result
		}()
	}
	close(start)
	waitGroup.Wait()
	close(results)
	close(errorsFound)

	for err := range errorsFound {
		t.Errorf("concurrent Shorten() error = %v", err)
	}
	var code string
	createdCount := 0
	resultCount := 0
	for result := range results {
		resultCount++
		if code == "" {
			code = result.Link.Code
		}
		if result.Link.Code != code {
			t.Errorf("got code %q; want all results to use %q", result.Link.Code, code)
		}
		if result.Created {
			createdCount++
		}
	}
	if resultCount != workers {
		t.Fatalf("successful results = %d; want %d", resultCount, workers)
	}
	if createdCount != 1 {
		t.Fatalf("created results = %d; want 1", createdCount)
	}
}

func TestConcurrentDuplicateWithSameCandidateReturnsWinner(t *testing.T) {
	store := openTestStore(t)
	generator := newBarrierGenerator("same_candidate", 2)
	service := shortener.NewService(store, generator)
	const workers = 2

	results := make(chan shortener.ShortenResult, workers)
	errorsFound := make(chan error, workers)
	var waitGroup sync.WaitGroup
	for i := 0; i < workers; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			result, err := service.Shorten(context.Background(), shortener.ShortenRequest{URL: "https://example.com/same-candidate"})
			if err != nil {
				errorsFound <- err
				return
			}
			results <- result
		}()
	}
	waitGroup.Wait()
	close(results)
	close(errorsFound)

	for err := range errorsFound {
		t.Errorf("concurrent Shorten() error = %v", err)
	}
	createdCount := 0
	resultCount := 0
	for result := range results {
		resultCount++
		if result.Link.Code != "same_candidate" {
			t.Errorf("code = %q; want same_candidate", result.Link.Code)
		}
		if result.Created {
			createdCount++
		}
	}
	if resultCount != workers || createdCount != 1 {
		t.Fatalf("results = %d, created = %d; want 2 results and 1 creation", resultCount, createdCount)
	}
}

func TestConcurrentAliasClaimHasOneWinner(t *testing.T) {
	store := openTestStore(t)
	service := shortener.NewService(store, staticGenerator{code: "unused"})
	const workers = 32
	alias := "shared_alias"

	start := make(chan struct{})
	errorsFound := make(chan error, workers)
	var successes atomic.Int64
	var waitGroup sync.WaitGroup
	for i := 0; i < workers; i++ {
		i := i
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			_, err := service.Shorten(context.Background(), shortener.ShortenRequest{
				URL:         fmt.Sprintf("https://example.com/%d", i),
				CustomAlias: &alias,
			})
			if err == nil {
				successes.Add(1)
				return
			}
			if !errors.Is(err, shortener.ErrAliasConflict) {
				errorsFound <- err
			}
		}()
	}
	close(start)
	waitGroup.Wait()
	close(errorsFound)

	for err := range errorsFound {
		t.Errorf("unexpected concurrent alias error = %v", err)
	}
	if got := successes.Load(); got != 1 {
		t.Fatalf("successful alias claims = %d; want 1", got)
	}
	if _, err := service.Resolve(context.Background(), alias); err != nil {
		t.Fatalf("winning alias was not stored: %v", err)
	}
}

func TestServiceReportsGeneratorFailure(t *testing.T) {
	store := openTestStore(t)
	cause := errors.New("entropy unavailable")
	service := shortener.NewService(store, errorGenerator{err: cause})

	_, err := service.Shorten(context.Background(), shortener.ShortenRequest{URL: "https://example.com"})
	if !errors.Is(err, shortener.ErrCodeGenerationFailed) {
		t.Fatalf("Shorten() error = %v; want ErrCodeGenerationFailed", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("Shorten() error = %v; want wrapped entropy cause", err)
	}
}

func openTestStore(t *testing.T) *filestore.Store {
	t.Helper()
	store, err := filestore.Open(filepath.Join(t.TempDir(), "links.json"))
	if err != nil {
		t.Fatalf("file.Open() error = %v", err)
	}
	return store
}

type scriptedGenerator struct {
	mu    sync.Mutex
	codes []string
	calls int
}

func (g *scriptedGenerator) Generate() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.calls >= len(g.codes) {
		return "", errors.New("scripted generator exhausted")
	}
	code := g.codes[g.calls]
	g.calls++
	return code, nil
}

func (g *scriptedGenerator) Calls() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

type staticGenerator struct {
	code string
}

func (g staticGenerator) Generate() (string, error) {
	return g.code, nil
}

type errorGenerator struct {
	err error
}

func (g errorGenerator) Generate() (string, error) {
	return "", g.err
}

type countingGenerator struct {
	value atomic.Uint64
}

func (g *countingGenerator) Generate() (string, error) {
	return fmt.Sprintf("generated_%d", g.value.Add(1)), nil
}

type barrierGenerator struct {
	code     string
	want     int64
	arrivals atomic.Int64
	release  chan struct{}
}

func newBarrierGenerator(code string, want int64) *barrierGenerator {
	return &barrierGenerator{code: code, want: want, release: make(chan struct{})}
}

func (g *barrierGenerator) Generate() (string, error) {
	if g.arrivals.Add(1) == g.want {
		close(g.release)
	}
	<-g.release
	return g.code, nil
}
