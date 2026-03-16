package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/haha-systems/conductor/internal/domain"
	"github.com/haha-systems/conductor/internal/provider"
)

// RunRequest is submitted to the supervisor to start a run.
type RunRequest struct {
	Run      *domain.Run
	Task     *domain.Task
	Provider provider.AgentProvider
	// GlobalEnv are extra env vars from conductor.toml [sandbox].
	GlobalEnv map[string]string
}

// Result is returned by the supervisor after a run terminates.
type Result struct {
	Run *domain.Run
	Err error
}

// Config holds supervisor configuration.
type Config struct {
	WorktreeBaseDir   string
	TimeoutMinutes    int
	PreserveOnFailure bool
	// RepoRoot is the root of the git repository.
	RepoRoot string
}

// Supervisor manages the lifecycle of a single run.
type Supervisor struct {
	cfg Config
}

func New(cfg Config) *Supervisor {
	return &Supervisor{cfg: cfg}
}

// Execute runs the task synchronously and returns when the run reaches a terminal state.
// The caller is responsible for collecting proof afterwards.
func (s *Supervisor) Execute(ctx context.Context, req RunRequest) *Result {
	run := req.Run
	now := time.Now()
	run.StartedAt = now
	run.Status = domain.RunStatusRunning

	worktreePath := filepath.Join(s.cfg.RepoRoot, s.cfg.WorktreeBaseDir, run.ID)
	run.WorktreePath = worktreePath

	// 1. Create git worktree.
	if err := s.createWorktree(worktreePath); err != nil {
		return s.fail(run, fmt.Errorf("create worktree: %w", err))
	}

	// 2. Write task prompt to file.
	taskFile := filepath.Join(worktreePath, ".conductor-task.md")
	if err := os.WriteFile(taskFile, []byte(req.Task.Description), 0600); err != nil {
		s.cleanup(run)
		return s.fail(run, fmt.Errorf("write task file: %w", err))
	}

	// 3. Open run log.
	if err := os.MkdirAll(filepath.Join(worktreePath, "proof"), 0755); err != nil {
		s.cleanup(run)
		return s.fail(run, fmt.Errorf("create proof dir: %w", err))
	}
	logPath := filepath.Join(worktreePath, "run.jsonl")
	logFile, err := os.Create(logPath)
	if err != nil {
		s.cleanup(run)
		return s.fail(run, fmt.Errorf("create run log: %w", err))
	}
	defer logFile.Close()

	logWriter := &providerLogWriter{w: logFile}

	// 4. Apply timeout.
	timeout := time.Duration(s.cfg.TimeoutMinutes) * time.Minute
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 5. Build env: merge global + per-task.
	env := mergeEnv(req.GlobalEnv, taskEnv(req.Task))

	rc := provider.RunContext{
		RepoPath:       worktreePath,
		TaskFile:       taskFile,
		Env:            env,
		LogWriter:      logWriter,
		TimeoutSeconds: int(timeout.Seconds()),
	}

	// 6. Launch provider.
	logEvent(logFile, "run_started", map[string]any{
		"run_id":   run.ID,
		"provider": req.Provider.Name(),
		"task_id":  run.TaskID,
	})

	handle, err := req.Provider.Run(runCtx, rc)
	if err != nil {
		s.cleanup(run)
		return s.fail(run, fmt.Errorf("launch provider: %w", err))
	}

	// 7. Wait for completion.
	// exec.CommandContext kills the process when runCtx expires, so Wait() will
	// return in all cases. We check runCtx.Err() after Wait() to distinguish
	// timeout from ordinary failure. We do NOT call Cancel() here — the process
	// is already dead once Wait() returns, and calling Cancel() would trigger a
	// second Wait() call on the same Cmd (which is unsafe).
	waitErr := handle.Wait()

	finished := time.Now()
	run.FinishedAt = &finished

	if runCtx.Err() == context.DeadlineExceeded {
		run.Status = domain.RunStatusTimeout
		logEvent(logFile, "run_timeout", map[string]any{"run_id": run.ID})
		if !s.cfg.PreserveOnFailure {
			s.cleanup(run)
		}
		return &Result{Run: run, Err: fmt.Errorf("run timed out after %d minutes", s.cfg.TimeoutMinutes)}
	}

	if waitErr != nil {
		run.Status = domain.RunStatusFailed
		run.ErrorMsg = waitErr.Error()
		logEvent(logFile, "run_failed", map[string]any{"run_id": run.ID, "error": waitErr.Error()})
		if !s.cfg.PreserveOnFailure {
			s.cleanup(run)
		}
		return &Result{Run: run, Err: waitErr}
	}

	run.Status = domain.RunStatusSucceeded
	logEvent(logFile, "run_succeeded", map[string]any{"run_id": run.ID})

	return &Result{Run: run}
}

// Cleanup removes the worktree. Called explicitly on success; may be skipped on failure.
func (s *Supervisor) Cleanup(run *domain.Run) error {
	return s.removeWorktree(run.WorktreePath)
}

func (s *Supervisor) createWorktree(path string) error {
	cmd := exec.Command("git", "worktree", "add", "--detach", path)
	cmd.Dir = s.cfg.RepoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

func (s *Supervisor) removeWorktree(path string) error {
	cmd := exec.Command("git", "worktree", "remove", "--force", path)
	cmd.Dir = s.cfg.RepoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

func (s *Supervisor) cleanup(run *domain.Run) {
	s.removeWorktree(run.WorktreePath) //nolint:errcheck
}

func (s *Supervisor) fail(run *domain.Run, err error) *Result {
	t := time.Now()
	run.Status = domain.RunStatusFailed
	run.FinishedAt = &t
	run.ErrorMsg = err.Error()
	return &Result{Run: run, Err: err}
}

// logEvent writes a structured JSON log line to the run log file.
func logEvent(w *os.File, event string, fields map[string]any) {
	fields["timestamp"] = time.Now().UTC().Format(time.RFC3339Nano)
	fields["event"] = event
	line, _ := json.Marshal(fields)
	w.Write(append(line, '\n')) //nolint:errcheck
}

// providerLogWriter wraps the run log file and writes each provider output
// chunk as a structured JSON log entry.
type providerLogWriter struct {
	w *os.File
}

func (lw *providerLogWriter) Write(p []byte) (int, error) {
	entry := map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"event":     "provider_stdout",
		"line":      string(p),
	}
	line, _ := json.Marshal(entry)
	lw.w.Write(append(line, '\n')) //nolint:errcheck
	return len(p), nil
}

func mergeEnv(global, perTask map[string]string) map[string]string {
	merged := make(map[string]string, len(global)+len(perTask))
	for k, v := range global {
		merged[k] = v
	}
	for k, v := range perTask {
		merged[k] = v
	}
	return merged
}

func taskEnv(task *domain.Task) map[string]string {
	if task.Config == nil {
		return nil
	}
	return task.Config.Env
}
