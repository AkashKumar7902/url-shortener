package file_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/meakash7902/url-shortener/internal/shortener"
	filestore "github.com/meakash7902/url-shortener/internal/store/file"
)

func TestMappingsPersistAcrossReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nested", "links.json")
	first, err := filestore.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	createdAt := time.Date(2026, time.July, 17, 4, 0, 0, 0, time.UTC)
	generated := shortener.Link{
		Code:      "generated_code",
		URL:       "https://example.com/a?b=2&a=1#fragment",
		Kind:      shortener.KindGenerated,
		CreatedAt: createdAt,
	}
	custom := shortener.Link{
		Code:      "Docs",
		URL:       generated.URL,
		Kind:      shortener.KindCustom,
		CreatedAt: createdAt.Add(time.Second),
	}
	if err := first.Insert(context.Background(), generated); err != nil {
		t.Fatalf("Insert(generated) error = %v", err)
	}
	if err := first.Insert(context.Background(), custom); err != nil {
		t.Fatalf("Insert(custom) error = %v", err)
	}

	second, err := filestore.Open(path)
	if err != nil {
		t.Fatalf("reopen store error = %v", err)
	}
	for _, want := range []shortener.Link{generated, custom} {
		got, err := second.GetByCode(context.Background(), want.Code)
		if err != nil {
			t.Fatalf("GetByCode(%q) error = %v", want.Code, err)
		}
		if got != want {
			t.Fatalf("GetByCode(%q) = %+v; want %+v", want.Code, got, want)
		}
	}
	gotGenerated, err := second.GetGeneratedByURL(context.Background(), generated.URL)
	if err != nil || gotGenerated.Code != generated.Code {
		t.Fatalf("GetGeneratedByURL() = %+v, %v; want code %q", gotGenerated, err, generated.Code)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(data file) error = %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("data-file permissions = %o; want 600", mode)
	}
}

func TestInsertEnforcesCodeAndGeneratedURLUniqueness(t *testing.T) {
	t.Parallel()

	store := openStore(t)
	createdAt := time.Now().UTC()
	first := shortener.Link{Code: "first", URL: "https://example.com", Kind: shortener.KindGenerated, CreatedAt: createdAt}
	if err := store.Insert(context.Background(), first); err != nil {
		t.Fatalf("Insert(first) error = %v", err)
	}

	sameCode := shortener.Link{Code: "first", URL: "https://other.example", Kind: shortener.KindCustom, CreatedAt: createdAt}
	if err := store.Insert(context.Background(), sameCode); !errors.Is(err, shortener.ErrCodeAlreadyExists) {
		t.Fatalf("same-code Insert() error = %v; want ErrCodeAlreadyExists", err)
	}

	sameGeneratedURL := shortener.Link{Code: "second", URL: first.URL, Kind: shortener.KindGenerated, CreatedAt: createdAt}
	if err := store.Insert(context.Background(), sameGeneratedURL); !errors.Is(err, shortener.ErrGeneratedURLAlreadyExists) {
		t.Fatalf("same-generated-URL Insert() error = %v; want ErrGeneratedURLAlreadyExists", err)
	}

	customSameURL := shortener.Link{Code: "custom", URL: first.URL, Kind: shortener.KindCustom, CreatedAt: createdAt}
	if err := store.Insert(context.Background(), customSameURL); err != nil {
		t.Fatalf("custom alias for existing URL should be allowed: %v", err)
	}
}

func TestStoreHonorsCancelledContext(t *testing.T) {
	t.Parallel()

	store := openStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := store.GetByCode(ctx, "missing"); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetByCode() error = %v; want context.Canceled", err)
	}
	link := shortener.Link{
		Code:      "code",
		URL:       "https://example.com",
		Kind:      shortener.KindGenerated,
		CreatedAt: time.Now().UTC(),
	}
	if err := store.Insert(ctx, link); !errors.Is(err, context.Canceled) {
		t.Fatalf("Insert() error = %v; want context.Canceled", err)
	}
}

func TestConcurrentDistinctInsertsAllSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "links.json")
	store, err := filestore.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	const workers = 40
	createdAt := time.Now().UTC()

	start := make(chan struct{})
	errorsFound := make(chan error, workers)
	var waitGroup sync.WaitGroup
	for i := 0; i < workers; i++ {
		i := i
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			errorsFound <- store.Insert(context.Background(), shortener.Link{
				Code:      fmt.Sprintf("code_%02d", i),
				URL:       fmt.Sprintf("https://example.com/%d", i),
				Kind:      shortener.KindCustom,
				CreatedAt: createdAt,
			})
		}()
	}
	close(start)
	waitGroup.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Errorf("concurrent Insert() error = %v", err)
		}
	}

	reopened, err := filestore.Open(path)
	if err != nil {
		t.Fatalf("reopen store error = %v", err)
	}
	for i := 0; i < workers; i++ {
		code := fmt.Sprintf("code_%02d", i)
		if _, err := reopened.GetByCode(context.Background(), code); err != nil {
			t.Errorf("GetByCode(%q) after reopen error = %v", code, err)
		}
	}
}

func TestOpenRejectsCorruptOrInconsistentData(t *testing.T) {
	t.Parallel()

	validTimestamp := "2026-07-17T04:00:00Z"
	cases := map[string]string{
		"unsupported version": `{"version":2,"links":[]}`,
		"unknown field":       `{"version":1,"links":[],"surprise":true}`,
		"trailing value":      `{"version":1,"links":[]} {}`,
		"duplicate code": `{"version":1,"links":[` +
			`{"code":"same","url":"https://one.example","kind":"custom","created_at":"` + validTimestamp + `"},` +
			`{"code":"same","url":"https://two.example","kind":"custom","created_at":"` + validTimestamp + `"}]}`,
		"duplicate generated URL": `{"version":1,"links":[` +
			`{"code":"one","url":"https://same.example","kind":"generated","created_at":"` + validTimestamp + `"},` +
			`{"code":"two","url":"https://same.example","kind":"generated","created_at":"` + validTimestamp + `"}]}`,
		"invalid destination": `{"version":1,"links":[` +
			`{"code":"one","url":"javascript:alert(1)","kind":"generated","created_at":"` + validTimestamp + `"}]}`,
	}

	for name, contents := range cases {
		name := name
		contents := contents
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "links.json")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			if _, err := filestore.Open(path); err == nil {
				t.Fatal("Open() accepted corrupt data")
			}
		})
	}
}

func TestSnapshotOrderIsDeterministic(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "links.json")
	store, err := filestore.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	createdAt := time.Now().UTC()
	for _, link := range []shortener.Link{
		{Code: "z-last", URL: "https://z.example", Kind: shortener.KindCustom, CreatedAt: createdAt},
		{Code: "a-first", URL: "https://a.example", Kind: shortener.KindCustom, CreatedAt: createdAt},
	} {
		if err := store.Insert(context.Background(), link); err != nil {
			t.Fatalf("Insert(%q) error = %v", link.Code, err)
		}
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if strings.Index(string(payload), `"a-first"`) > strings.Index(string(payload), `"z-last"`) {
		t.Fatalf("snapshot is not sorted by code:\n%s", payload)
	}
}

func openStore(t *testing.T) *filestore.Store {
	t.Helper()
	store, err := filestore.Open(filepath.Join(t.TempDir(), "links.json"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return store
}
