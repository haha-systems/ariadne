package mcpserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haha-systems/ariadne/internal/runstate"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServerExposesRunResourcesAndRefreshTool(t *testing.T) {
	repoRoot := t.TempDir()
	worktreeDir := ".ariadne/runs"
	runDir := filepath.Join(repoRoot, worktreeDir, "run_123")
	if err := os.MkdirAll(filepath.Join(runDir, "proof"), 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	logBody := strings.Join([]string{
		`{"timestamp":"2026-04-09T20:00:00Z","event":"run_started","run_id":"run_123","provider":"codex","task_id":"HAHA-1"}`,
		`{"timestamp":"2026-04-09T20:01:00Z","event":"provider_stdout","line":"working"}`,
		`{"timestamp":"2026-04-09T20:02:00Z","event":"run_succeeded","run_id":"run_123"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(runDir, "run.jsonl"), []byte(logBody), 0o644); err != nil {
		t.Fatalf("write run log: %v", err)
	}
	proofBody := `{"run_id":"run_123","task_id":"HAHA-1","provider":"codex","pr_url":"http://example/pr/1","walkthrough":"done"}`
	if err := os.WriteFile(filepath.Join(runDir, "proof", "summary.json"), []byte(proofBody), 0o644); err != nil {
		t.Fatalf("write proof summary: %v", err)
	}

	cfg := Config{
		RepoRoot:      repoRoot,
		WorktreeDir:   worktreeDir,
		RunStatePath:  filepath.Join(repoRoot, worktreeDir, "index.json"),
		ListenAddress: "127.0.0.1:7619",
		MCPPath:       "/mcp",
	}
	server := New(cfg, Options{Now: func() time.Time { return time.Date(2026, 4, 9, 20, 3, 0, 0, time.UTC) }})

	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: httpServer.URL + "/mcp",
	}, nil)
	if err != nil {
		t.Fatalf("connect mcp client: %v", err)
	}
	defer session.Close()

	overview, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: resourceOverview})
	if err != nil {
		t.Fatalf("read overview: %v", err)
	}
	if !strings.Contains(firstResourceText(t, overview), `"service": "ariadne-mcp"`) {
		t.Fatalf("unexpected overview: %s", firstResourceText(t, overview))
	}

	toolResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "refresh_run_index",
		Arguments: map[string]any{"limit": 10},
	})
	if err != nil {
		t.Fatalf("call refresh tool: %v", err)
	}
	if toolResult.IsError {
		t.Fatalf("unexpected tool error: %#v", toolResult)
	}

	runsResource, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: resourceRuns})
	if err != nil {
		t.Fatalf("read runs resource: %v", err)
	}
	var runs []runstate.Record
	if err := json.Unmarshal([]byte(firstResourceText(t, runsResource)), &runs); err != nil {
		t.Fatalf("decode runs resource: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "run_123" {
		t.Fatalf("unexpected runs payload: %#v", runs)
	}

	detail, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "ariadne://runs/run_123"})
	if err != nil {
		t.Fatalf("read run detail: %v", err)
	}
	if !strings.Contains(firstResourceText(t, detail), `"pr_url": "http://example/pr/1"`) {
		t.Fatalf("unexpected run detail: %s", firstResourceText(t, detail))
	}

	logs, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "ariadne://runs/run_123/logs"})
	if err != nil {
		t.Fatalf("read run logs: %v", err)
	}
	if !strings.Contains(firstResourceText(t, logs), `"event": "provider_stdout"`) {
		t.Fatalf("unexpected run logs payload: %s", firstResourceText(t, logs))
	}
}

func firstResourceText(t *testing.T, result *mcp.ReadResourceResult) string {
	t.Helper()
	if len(result.Contents) == 0 {
		t.Fatal("expected resource contents")
	}
	return result.Contents[0].Text
}
