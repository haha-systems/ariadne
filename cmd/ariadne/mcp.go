package main

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/haha-systems/ariadne/internal/config"
	"github.com/haha-systems/ariadne/internal/mcpserver"
	"github.com/haha-systems/ariadne/internal/operator"
)

func mcpCmd(cfgPath *string) *cobra.Command {
	var listenAddr string
	var mcpPath string

	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve Ariadne's MCP operator plane over local streamable HTTP",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = godotenv.Load()
			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}
			if listenAddr == "" {
				listenAddr = "127.0.0.1:7618"
			}
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			operatorSvc, err := operator.New(cfg, repoRoot())
			if err != nil {
				return err
			}

			server := mcpserver.New(mcpserver.Config{
				RepoRoot:      repoRoot(),
				WorktreeDir:   cfg.Sandbox.WorktreeDir,
				RunStatePath:  runStateStore(cfg).Path(),
				ListenAddress: listenAddr,
				MCPPath:       mcpPath,
				Operator:      operatorSvc,
			}, mcpserver.Options{})

			fmt.Fprintf(cmd.OutOrStdout(), "Ariadne MCP listening on http://%s%s\n", listenAddr, normalizeMCPPath(mcpPath))
			return server.ListenAndServe(ctx)
		},
	}
	cmd.Flags().StringVar(&listenAddr, "listen", "127.0.0.1:7618", "listen address for the MCP server")
	cmd.Flags().StringVar(&mcpPath, "path", "/mcp", "HTTP path for the MCP endpoint")
	return cmd
}

func normalizeMCPPath(path string) string {
	if path == "" {
		return "/mcp"
	}
	if path[0] != '/' {
		return "/" + path
	}
	return path
}
