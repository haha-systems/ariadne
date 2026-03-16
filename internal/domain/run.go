package domain

import "time"

// RunStatus represents the lifecycle state of a single run attempt.
type RunStatus string

const (
	RunStatusPending   RunStatus = "pending"
	RunStatusRunning   RunStatus = "running"
	RunStatusSucceeded RunStatus = "succeeded"
	RunStatusFailed    RunStatus = "failed"
	RunStatusTimeout   RunStatus = "timeout"
)

// Run is a single attempt to implement a task by a provider.
type Run struct {
	ID           string
	TaskID       string
	Provider     string
	WorktreePath string
	Status       RunStatus
	StartedAt    time.Time
	FinishedAt   *time.Time
	ErrorMsg     string
}

// ProofBundle holds the proof-of-work artifacts collected after a successful run.
type ProofBundle struct {
	RunID           string   `json:"run_id"`
	TaskID          string   `json:"task_id"`
	Provider        string   `json:"provider"`
	DurationSeconds float64  `json:"duration_seconds"`
	CostUSD         float64  `json:"cost_usd"`
	CI              CIResult `json:"ci"`
	Diff            DiffStat `json:"diff"`
	PRUrl           string   `json:"pr_url"`
	Walkthrough     string   `json:"walkthrough"`
}

// CIResult holds the outcome of running the project's test suite.
type CIResult struct {
	Passed    bool `json:"passed"`
	TestCount int  `json:"test_count"`
	Failures  int  `json:"failures"`
}

// DiffStat summarises the changes produced by a run.
type DiffStat struct {
	FilesChanged int `json:"files_changed"`
	Insertions   int `json:"insertions"`
	Deletions    int `json:"deletions"`
}
