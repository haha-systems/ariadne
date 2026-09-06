package main

import "testing"

func TestRootCmd_HasExpectedSubcommands(t *testing.T) {
	root := rootCmd()
	want := []string{"run", "collect-proof", "land", "cost", "research", "agents", "budget", "escalations"}
	for _, name := range want {
		if cmd, _, err := root.Find([]string{name}); err != nil || cmd.Name() != name {
			t.Errorf("expected subcommand %q, err=%v", name, err)
		}
	}
}

func TestRootCmd_HasExpectedResearchSubtree(t *testing.T) {
	root := rootCmd()
	want := [][]string{{"research", "run"}, {"research", "status"}, {"research", "frontier"}}
	for _, path := range want {
		if cmd, _, err := root.Find(path); err != nil || cmd.Name() != path[len(path)-1] {
			t.Errorf("expected subcommand %v, err=%v", path, err)
		}
	}
}

func TestRootCmd_HasExpectedEscalationsSubtree(t *testing.T) {
	root := rootCmd()
	want := [][]string{{"escalations", "list"}, {"escalations", "show"}, {"escalations", "explain"}}
	for _, path := range want {
		if cmd, _, err := root.Find(path); err != nil || cmd.Name() != path[len(path)-1] {
			t.Errorf("expected subcommand %v, err=%v", path, err)
		}
	}
}

func TestRootCmd_ConfigFlagDefault(t *testing.T) {
	root := rootCmd()
	flag := root.PersistentFlags().Lookup("config")
	if flag == nil {
		t.Fatal("expected a --config persistent flag")
	}
	if flag.DefValue != "ariadne.toml" {
		t.Errorf("default config path = %q, want ariadne.toml", flag.DefValue)
	}
}

func TestCollectProofCmd_RequiresRunID(t *testing.T) {
	var cfgPath string
	cmd := collectProofCmd(&cfgPath)
	if ann := cmd.Flags().Lookup("run-id").Annotations["cobra_annotation_bash_completion_one_required_flag"]; ann == nil {
		t.Error("expected run-id to be marked required")
	}
}

func TestLandCmd_RequiresRunID(t *testing.T) {
	var cfgPath string
	cmd := landCmd(&cfgPath)
	if ann := cmd.Flags().Lookup("run-id").Annotations["cobra_annotation_bash_completion_one_required_flag"]; ann == nil {
		t.Error("expected run-id to be marked required")
	}
}
