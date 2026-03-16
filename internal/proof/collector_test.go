package proof

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseShortstat(t *testing.T) {
	cases := []struct {
		input      string
		files, ins, del int
	}{
		{" 4 files changed, 87 insertions(+), 12 deletions(-)", 4, 87, 12},
		{" 1 file changed, 3 insertions(+)", 1, 3, 0},
		{" 2 files changed, 5 deletions(-)", 2, 0, 5},
		{"", 0, 0, 0},
	}

	for _, tc := range cases {
		stat := parseShortstat(tc.input)
		if stat.FilesChanged != tc.files || stat.Insertions != tc.ins || stat.Deletions != tc.del {
			t.Errorf("parseShortstat(%q) = {%d,%d,%d}, want {%d,%d,%d}",
				tc.input, stat.FilesChanged, stat.Insertions, stat.Deletions,
				tc.files, tc.ins, tc.del)
		}
	}
}

func TestParseTestOutput(t *testing.T) {
	output := "--- PASS: TestFoo (0.01s)\n--- PASS: TestBar (0.00s)\n--- FAIL: TestBaz (0.03s)\nFAIL\n"
	total, failures := parseTestOutput(output)
	if total != 3 {
		t.Errorf("expected total=3, got %d", total)
	}
	if failures != 1 {
		t.Errorf("expected failures=1, got %d", failures)
	}
}

func TestInferCICommand_GoMod(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test"), 0644)
	cmd := inferCICommand(dir)
	if len(cmd) == 0 || cmd[0] != "go" {
		t.Errorf("expected go test command, got %v", cmd)
	}
}

func TestInferCICommand_PackageJSON(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0644)
	cmd := inferCICommand(dir)
	if len(cmd) == 0 || cmd[0] != "npm" {
		t.Errorf("expected npm test command, got %v", cmd)
	}
}

func TestInferCICommand_Unknown(t *testing.T) {
	cmd := inferCICommand(t.TempDir())
	if cmd != nil {
		t.Errorf("expected nil for unknown repo type, got %v", cmd)
	}
}
