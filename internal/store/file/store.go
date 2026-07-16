// Package file provides a small, durable datastore for a single service
// process. Mutations are serialized, written to a same-directory temporary
// file, flushed, and atomically renamed over the previous snapshot.
package file

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/meakash7902/url-shortener/internal/shortener"
)

const stateVersion = 1

type persistedState struct {
	Version int              `json:"version"`
	Links   []shortener.Link `json:"links"`
}

type Store struct {
	mu             sync.RWMutex
	path           string
	linksByCode    map[string]shortener.Link
	generatedByURL map[string]string
	syncDirectory  func(string) error
	durabilityErr  error
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("data-file path is required")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}

	store := &Store{
		path:           path,
		linksByCode:    make(map[string]shortener.Link),
		generatedByURL: make(map[string]string),
		syncDirectory:  syncDirectory,
	}
	if err := store.load(); err != nil {
		return nil, err
	}

	return store, nil
}

func (s *Store) GetByCode(ctx context.Context, code string) (shortener.Link, error) {
	if err := ctx.Err(); err != nil {
		return shortener.Link{}, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.durabilityErr != nil {
		return shortener.Link{}, fmt.Errorf("datastore unavailable after durability failure: %w", s.durabilityErr)
	}

	link, ok := s.linksByCode[code]
	if !ok {
		return shortener.Link{}, shortener.ErrNotFound
	}
	return link, nil
}

func (s *Store) GetGeneratedByURL(ctx context.Context, rawURL string) (shortener.Link, error) {
	if err := ctx.Err(); err != nil {
		return shortener.Link{}, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.durabilityErr != nil {
		return shortener.Link{}, fmt.Errorf("datastore unavailable after durability failure: %w", s.durabilityErr)
	}

	code, ok := s.generatedByURL[rawURL]
	if !ok {
		return shortener.Link{}, shortener.ErrNotFound
	}
	return s.linksByCode[code], nil
}

// Insert atomically enforces a shared code namespace and at most one generated
// mapping per exact URL. It never overwrites an existing mapping.
func (s *Store) Insert(ctx context.Context, link shortener.Link) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateLink(link); err != nil {
		return fmt.Errorf("validate link before insert: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	if s.durabilityErr != nil {
		return fmt.Errorf("datastore unavailable after durability failure: %w", s.durabilityErr)
	}
	if link.Kind == shortener.KindGenerated {
		if _, exists := s.generatedByURL[link.URL]; exists {
			return shortener.ErrGeneratedURLAlreadyExists
		}
	}
	if _, exists := s.linksByCode[link.Code]; exists {
		return shortener.ErrCodeAlreadyExists
	}

	nextByCode := cloneLinks(s.linksByCode)
	nextByCode[link.Code] = link

	committed, err := s.writeSnapshot(nextByCode)
	if committed {
		// Once rename succeeds, the new snapshot is visible even if syncing the
		// directory metadata reports an error. Keep memory consistent with disk.
		s.linksByCode = nextByCode
		if link.Kind == shortener.KindGenerated {
			s.generatedByURL[link.URL] = link.Code
		}
		if err != nil {
			// The rename made the state visible, but its directory entry could
			// not be confirmed durable. Refuse later acknowledgements until a
			// clean reopen instead of returning 200 for uncertain state.
			s.durabilityErr = err
		}
	}
	if err != nil {
		return fmt.Errorf("persist link: %w", err)
	}

	return nil
}

func (s *Store) load() error {
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open data file: %w", err)
	}
	defer f.Close()

	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()

	var state persistedState
	if err := decoder.Decode(&state); err != nil {
		return fmt.Errorf("decode data file: %w", err)
	}
	if err := expectJSONEnd(decoder); err != nil {
		return fmt.Errorf("decode data file: %w", err)
	}
	if state.Version != stateVersion {
		return fmt.Errorf("unsupported data-file version %d", state.Version)
	}

	for index, link := range state.Links {
		if err := validateLink(link); err != nil {
			return fmt.Errorf("invalid link at index %d: %w", index, err)
		}
		if _, exists := s.linksByCode[link.Code]; exists {
			return fmt.Errorf("duplicate code %q in data file", link.Code)
		}
		if link.Kind == shortener.KindGenerated {
			if _, exists := s.generatedByURL[link.URL]; exists {
				return fmt.Errorf("duplicate generated URL at index %d", index)
			}
			s.generatedByURL[link.URL] = link.Code
		}
		s.linksByCode[link.Code] = link
	}

	return nil
}

// writeSnapshot reports committed=true after the atomic rename. An error can
// still follow if the filesystem cannot sync the containing directory.
func (s *Store) writeSnapshot(links map[string]shortener.Link) (committed bool, err error) {
	state := persistedState{
		Version: stateVersion,
		Links:   make([]shortener.Link, 0, len(links)),
	}
	for _, link := range links {
		state.Links = append(state.Links, link)
	}
	sort.Slice(state.Links, func(i, j int) bool {
		return state.Links[i].Code < state.Links[j].Code
	})

	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return false, fmt.Errorf("encode snapshot: %w", err)
	}
	payload = append(payload, '\n')

	directory := filepath.Dir(s.path)
	temporary, err := os.CreateTemp(directory, ".links-*.tmp")
	if err != nil {
		return false, fmt.Errorf("create temporary snapshot: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(0o600); err != nil {
		return false, fmt.Errorf("set snapshot permissions: %w", err)
	}
	written, err := temporary.Write(payload)
	if err != nil {
		return false, fmt.Errorf("write snapshot: %w", err)
	}
	if written != len(payload) {
		return false, io.ErrShortWrite
	}
	if err := temporary.Sync(); err != nil {
		return false, fmt.Errorf("sync snapshot: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return false, fmt.Errorf("close snapshot: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return false, fmt.Errorf("replace snapshot: %w", err)
	}
	committed = true

	if err := s.syncDirectory(directory); err != nil {
		return true, err
	}

	return true, nil
}

func syncDirectory(directory string) error {
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open data directory for sync: %w", err)
	}
	defer directoryHandle.Close()
	if err := directoryHandle.Sync(); err != nil {
		return fmt.Errorf("sync data directory: %w", err)
	}
	return nil
}

func validateLink(link shortener.Link) error {
	if err := shortener.ValidateAlias(link.Code); err != nil {
		return fmt.Errorf("invalid code: %w", err)
	}
	if _, err := shortener.ValidateURL(link.URL); err != nil {
		return err
	}
	if link.Kind != shortener.KindGenerated && link.Kind != shortener.KindCustom {
		return fmt.Errorf("invalid link kind %q", link.Kind)
	}
	if link.CreatedAt.IsZero() {
		return fmt.Errorf("creation time is required")
	}
	return nil
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

func cloneLinks(source map[string]shortener.Link) map[string]shortener.Link {
	clone := make(map[string]shortener.Link, len(source)+1)
	for code, link := range source {
		clone[code] = link
	}
	return clone
}
