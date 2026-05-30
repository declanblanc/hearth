package media

import (
	"context"
	"io"
	"sync"
)

// Stub is an in-memory Store for tests and development. It never talks to R2.
type Stub struct {
	mu      sync.Mutex
	uploads map[string][]byte
	deleted []string
}

func NewStub() *Stub { return &Stub{uploads: map[string][]byte{}} }

func (s *Stub) Upload(_ context.Context, key string, r io.Reader, _ string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.uploads[key] = data
	s.mu.Unlock()
	return nil
}

func (s *Stub) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	delete(s.uploads, key)
	s.deleted = append(s.deleted, key)
	s.mu.Unlock()
	return nil
}

func (s *Stub) URL(key string) string {
	if key == "" {
		return ""
	}
	return "https://stub.test/" + key
}

// Has reports whether key is currently stored (not deleted).
func (s *Stub) Has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.uploads[key]
	return ok
}

// Deleted returns a copy of all keys that have been deleted.
func (s *Stub) Deleted() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.deleted...)
}
