package file

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/meakash7902/url-shortener/internal/shortener"
)

func TestPostRenameSyncFailurePoisonsStoreUntilReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "links.json")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	cause := errors.New("injected directory sync failure")
	store.syncDirectory = func(string) error { return cause }
	link := shortener.Link{
		Code:      "committed_code",
		URL:       "https://example.com/committed",
		Kind:      shortener.KindGenerated,
		CreatedAt: time.Now().UTC(),
	}

	if err := store.Insert(context.Background(), link); !errors.Is(err, cause) {
		t.Fatalf("Insert() error = %v; want injected sync failure", err)
	}
	if _, err := store.GetByCode(context.Background(), link.Code); !errors.Is(err, cause) {
		t.Fatalf("GetByCode() error = %v; poisoned store must not acknowledge uncertain durability", err)
	}
	if _, err := store.GetGeneratedByURL(context.Background(), link.URL); !errors.Is(err, cause) {
		t.Fatalf("GetGeneratedByURL() error = %v; poisoned store must not acknowledge uncertain durability", err)
	}

	// Rename completed before the injected failure, so a clean reopen can
	// validate and serve the visible snapshot again.
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after injected failure: %v", err)
	}
	got, err := reopened.GetByCode(context.Background(), link.Code)
	if err != nil || got != link {
		t.Fatalf("reopened GetByCode() = %+v, %v; want committed link", got, err)
	}
}
