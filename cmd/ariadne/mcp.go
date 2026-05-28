package main

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/haha-systems/ariadne/internal/config"
	"github.com/haha-systems/ariadne/internal/gateway"
	"github.com/haha-systems/ariadne/internal/mcpserver"
)

func mcpCmd(cfgPath *string) *cobra.Command {
	var listenAddr string
	var mcpPath string

	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve Ariadne's MCP gateway adapter over local streamable HTTP",
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

			// Build the gateway (thin adapter path for direct/agent-driven MCP runs).
			// No legacy operator.Service is created here.
			//
			// The old heavy operator (internal/operator) and the duplicated wiring
			// in cmd/ariadne/main.go:startOrchestrator are the legacy control plane,
			// kept only for backward compatibility with the pre-gateway `ariadne run`
			// polling path and any direct construction of operator.Service.
			providers := gateway.BuildProvidersFromConfig(cfg)
			sup := gateway.DefaultSupervisorForGateway(cfg, repoRoot())
			exec := gateway.NewSupervisorExecutor(repoRoot(), sup, providers, cfg.Personas)

			gw, err := gateway.New(gateway.Config{
				RepoRoot:        repoRoot(),
				DefaultProvider: cfg.Ariadne.DefaultProvider,
			}, exec)
			if err != nil {
				return fmt.Errorf("create gateway: %w", err)
			}

			server := mcpserver.New(mcpserver.Config{
				RepoRoot:        repoRoot(),
				WorktreeDir:     cfg.Sandbox.WorktreeDir,
				RunStatePath:    runStateStore(cfg).Path(),
				MemoryStorePath: memoryStore(cfg).Path(),
				Skills:          cfg.Skills,
				ListenAddress:   listenAddr,
				MCPPath:         mcpPath,
				Gateway:         gw,
				// Operator left nil: start_run/cancel_run now go exclusively through Gateway
				// (the legacy Operator fallback in mcpserver remains only for
				// hypothetical external callers still passing an *operator.Service).
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
