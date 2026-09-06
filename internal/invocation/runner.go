package invocation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/haha-systems/ariadne/internal/provider"
)

// Request is a bounded request for one agent to perform one cognitive
// operation. It does not assume a GitHub issue, a PR, or any other
// workflow-specific concept.
type Request struct {
	// Prompt is the full text handed to the agent.
	Prompt string
	// Env holds additional environment variables for the subprocess.
	Env map[string]string
	// TimeoutSeconds is the wall-clock timeout for the invocation.
	TimeoutSeconds int
	// OutputFile is where the agent should write its Envelope JSON. If
	// empty, ProviderRunner picks a default path inside the workspace.
	OutputFile string
	// Depth is 0 for a routine invocation and >=1 for an invocation made on
	// behalf of an escalation. Schedulers use this to prevent a scarce agent
	// from recursively escalating to itself.
	Depth int
}

// Runner invokes one agent against one Request and returns its Envelope.
type Runner interface {
	Invoke(ctx context.Context, req Request) (Envelope, error)
}

// ProviderRunner adapts an existing provider.AgentProvider into a Runner.
type ProviderRunner struct {
	Provider provider.AgentProvider
	// RepoPath is the working directory the provider subprocess runs in
	// (an isolated worktree, an ephemeral tempdir, or any workspace path).
	RepoPath string
	// LogWriter receives the provider's stdout/stderr. Optional.
	LogWriter io.Writer
}

// Invoke writes req.Prompt to a task file inside RepoPath, launches the
// provider, waits for it to exit, and reads back the Envelope it wrote (if
// any). A missing output file is treated as StatusComplete so that today's
// providers, which don't know about envelopes, keep working unchanged.
func (r ProviderRunner) Invoke(ctx context.Context, req Request) (Envelope, error) {
	taskFile := filepath.Join(r.RepoPath, ".ariadne-invocation.md")
	if err := os.WriteFile(taskFile, []byte(req.Prompt), 0600); err != nil {
		return Envelope{}, fmt.Errorf("invocation: write task file: %w", err)
	}

	outputFile := req.OutputFile
	if outputFile == "" {
		outputFile = filepath.Join(r.RepoPath, ".ariadne-envelope.json")
	}

	rc := provider.RunContext{
		RepoPath:       r.RepoPath,
		TaskFile:       taskFile,
		Env:            req.Env,
		LogWriter:      r.LogWriter,
		TimeoutSeconds: req.TimeoutSeconds,
		OutputFile:     outputFile,
	}

	handle, err := r.Provider.Run(ctx, rc)
	if err != nil {
		return Envelope{}, fmt.Errorf("invocation: launch %s: %w", r.Provider.Name(), err)
	}

	if waitErr := handle.Wait(); waitErr != nil {
		return Envelope{}, fmt.Errorf("invocation: %s: %w", r.Provider.Name(), waitErr)
	}

	return readEnvelope(outputFile)
}

// readEnvelope reads and parses the Envelope an agent wrote to path. A
// missing file is not an error — it means the agent didn't write a
// structured result, so the invocation is treated as complete.
func readEnvelope(path string) (Envelope, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Envelope{Status: StatusComplete}, nil
		}
		return Envelope{}, fmt.Errorf("invocation: read envelope: %w", err)
	}

	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return Envelope{}, fmt.Errorf("invocation: parse envelope: %w", err)
	}
	return env, nil
}
