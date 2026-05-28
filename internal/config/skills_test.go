package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverSkills(t *testing.T) {
	repoRoot := t.TempDir()
	skillsDir := filepath.Join(repoRoot, ".ariadne", "skills")
	
	// Create a workspace skill
	skill1Dir := filepath.Join(skillsDir, "workspace-skill")
	if err := os.MkdirAll(skill1Dir, 0755); err != nil {
		t.Fatal(err)
	}
	skill1Content := `---
name: ws-skill
description: A workspace skill
---
Body content
`
	if err := os.WriteFile(filepath.Join(skill1Dir, "SKILL.md"), []byte(skill1Content), 0644); err != nil {
		t.Fatal(err)
	}

	skills := DiscoverSkills(repoRoot)
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}

	s, ok := skills["ws-skill"]
	if !ok {
		t.Fatal("ws-skill not found")
	}
	if s.Description != "A workspace skill" {
		t.Errorf("expected description 'A workspace skill', got %q", s.Description)
	}
	if s.Dir != skill1Dir {
		t.Errorf("expected dir %q, got %q", skill1Dir, s.Dir)
	}
	if !s.IsPackage {
		t.Error("expected IsPackage to be true")
	}
}
