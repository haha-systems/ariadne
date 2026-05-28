package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"go.starlark.net/lib/json"
	"go.starlark.net/starlark"
)

// LoadCommandPlugins scans for Starlark scripts in .ariadne/commands/ and
// registers them as subcommands on the root command.
func LoadCommandPlugins(root *cobra.Command, repoRoot string) error {
	commandsDir := filepath.Join(repoRoot, ".ariadne", "commands")
	entries, err := os.ReadDir(commandsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".star") {
			continue
		}

		path := filepath.Join(commandsDir, entry.Name())
		if err := registerStarlarkCommand(root, path); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to load command plugin %s: %v\n", path, err)
		}
	}

	return nil
}

func registerStarlarkCommand(root *cobra.Command, path string) error {
	// Add built-ins for the execution environment
	predeclared := starlark.StringDict{
		"json": json.Module,
		"read_file": starlark.NewBuiltin("read_file", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var path string
			if err := starlark.UnpackArgs("read_file", args, kwargs, "path", &path); err != nil {
				return starlark.None, err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return starlark.None, err
			}
			return starlark.String(string(data)), nil
		}),
	}

	thread := &starlark.Thread{
		Name: "ariadne-command",
		Print: func(_ *starlark.Thread, msg string) {
			fmt.Println(msg)
		},
	}
	globals, err := starlark.ExecFile(thread, path, nil, predeclared)
	if err != nil {
		return err
	}

	// Required: run(args) function
	runFunc, ok := globals["run"]
	if !ok {
		return fmt.Errorf("plugin %s missing 'run(args)' function", path)
	}

	// Optional: name (defaults to filename without .star)
	name := strings.TrimSuffix(filepath.Base(path), ".star")
	if v, ok := globals["name"]; ok {
		if s, ok := v.(starlark.String); ok {
			name = s.GoString()
		}
	}

	// Optional: description
	description := "Custom Starlark command"
	if v, ok := globals["description"]; ok {
		if s, ok := v.(starlark.String); ok {
			description = s.GoString()
		}
	}

	cmd := &cobra.Command{
		Use:   name,
		Short: description,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Convert Go args to Starlark list
			starArgs := starlark.NewList(nil)
			for _, arg := range args {
				starArgs.Append(starlark.String(arg))
			}

			_, err := starlark.Call(thread, runFunc, starlark.Tuple{starArgs}, nil)
			return err
		},
	}

	root.AddCommand(cmd)
	return nil
}