package runstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/haha-systems/ariadne/internal/domain"
)

// Record holds the machine-readable state for a single Ariadne run.
type Record struct {
	ID              string            `json:"id"`
	TaskID          string            `json:"task_id"`
	TaskTitle       string            `json:"task_title"`
	TaskType        domain.TaskType   `json:"task_type"`
	TaskSource      string            `json:"task_source"`
	SourceURL       string            `json:"source_url,omitempty"`
	Provider        string            `json:"provider"`
	Persona         string            `json:"persona,omitempty"`
	Status          domain.RunStatus  `json:"status"`
	WorktreePath    string            `json:"worktree_path,omitempty"`
	LogPath         string            `json:"log_path,omitempty"`
	ProofPath       string            `json:"proof_path,omitempty"`
	PRURL           string            `json:"pr_url,omitempty"`
	Walkthrough     string            `json:"walkthrough,omitempty"`
	LastEvent       string            `json:"last_event,omitempty"`
	LastError       string            `json:"last_error,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	StartedAt       time.Time         `json:"started_at,omitempty"`
	UpdatedAt       time.Time         `json:"updated_at"`
	FinishedAt      *time.Time        `json:"finished_at,omitempty"`
	DurationSeconds float64           `json:"duration_seconds,omitempty"`
}

// Snapshot is the serialized monitor view written to disk.
type Snapshot struct {
	UpdatedAt time.Time `json:"updated_at"`
	Runs      []Record  `json:"runs"`
}

// Store keeps a persistent JSON index of recent runs.
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

func (s *Store) Upsert(record Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot, err := s.load()
	if err != nil {
		return err
	}

	record.UpdatedAt = time.Now().UTC()
	replaced := false
	for i := range snapshot.Runs {
		if snapshot.Runs[i].ID == record.ID {
			record.CreatedAt = snapshot.Runs[i].CreatedAt
			if record.CreatedAt.IsZero() {
				record.CreatedAt = record.UpdatedAt
			}
			snapshot.Runs[i] = record
			replaced = true
			break
		}
	}
	if !replaced {
		if record.CreatedAt.IsZero() {
			record.CreatedAt = record.UpdatedAt
		}
		snapshot.Runs = append(snapshot.Runs, record)
	}

	snapshot.UpdatedAt = record.UpdatedAt
	sort.Slice(snapshot.Runs, func(i, j int) bool {
		return snapshot.Runs[i].UpdatedAt.After(snapshot.Runs[j].UpdatedAt)
	})

	return s.save(snapshot)
}

func (s *Store) Update(runID string, mutate func(*Record) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot, err := s.load()
	if err != nil {
		return err
	}

	for i := range snapshot.Runs {
		if snapshot.Runs[i].ID != runID {
			continue
		}
		if err := mutate(&snapshot.Runs[i]); err != nil {
			return err
		}
		snapshot.Runs[i].UpdatedAt = time.Now().UTC()
		snapshot.UpdatedAt = snapshot.Runs[i].UpdatedAt
		sort.Slice(snapshot.Runs, func(i, j int) bool {
			return snapshot.Runs[i].UpdatedAt.After(snapshot.Runs[j].UpdatedAt)
		})
		return s.save(snapshot)
	}

	return fmt.Errorf("runstate: run %s not found", runID)
}

func (s *Store) List() ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]Record, len(snapshot.Runs))
	copy(out, snapshot.Runs)
	return out, nil
}

func (s *Store) Get(runID string) (*Record, error) {
	runs, err := s.List()
	if err != nil {
		return nil, err
	}
	for i := range runs {
		if runs[i].ID == runID {
			rec := runs[i]
			return &rec, nil
		}
	}
	return nil, fmt.Errorf("runstate: run %s not found", runID)
}

func (s *Store) load() (*Snapshot, error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return nil, fmt.Errorf("runstate: mkdir: %w", err)
	}

	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Snapshot{Runs: []Record{}}, nil
		}
		return nil, fmt.Errorf("runstate: read: %w", err)
	}

	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("runstate: decode: %w", err)
	}
	if snapshot.Runs == nil {
		snapshot.Runs = []Record{}
	}
	return &snapshot, nil
}

func (s *Store) save(snapshot *Snapshot) error {
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("runstate: encode: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("runstate: write temp: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("runstate: rename: %w", err)
	}
	return nil
}
