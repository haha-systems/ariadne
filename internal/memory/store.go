package memory

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Entry represents a single piece of persistent knowledge.
type Entry struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	RunID     string    `json:"run_id,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// Store handles persistent storage of harness-wide memory.
type Store struct {
	path string
	mu   sync.Mutex
}

func New(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) Remember(key, value, runID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.load()
	if err != nil {
		return err
	}

	entry := Entry{
		Key:       key,
		Value:     value,
		RunID:     runID,
		Timestamp: time.Now().UTC(),
	}

	found := false
	for i := range data {
		if data[i].Key == key {
			data[i] = entry
			found = true
			break
		}
	}
	if !found {
		data = append(data, entry)
	}

	sort.Slice(data, func(i, j int) bool {
		return data[i].Timestamp.After(data[j].Timestamp)
	})

	return s.save(data)
}

func (s *Store) Recall(key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.load()
	if err != nil {
		return "", false, err
	}

	for _, e := range data {
		if e.Key == key {
			return e.Value, true, nil
		}
	}
	return "", false, nil
}

func (s *Store) List() ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

func (s *Store) Forget(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.load()
	if err != nil {
		return err
	}

	newData := make([]Entry, 0, len(data))
	for _, e := range data {
		if e.Key != key {
			newData = append(newData, e)
		}
	}

	return s.save(newData)
}

func (s *Store) load() ([]Entry, error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return nil, err
	}

	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Entry{}, nil
		}
		return nil, err
	}

	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func (s *Store) save(entries []Entry) error {
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
