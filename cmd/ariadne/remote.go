package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

func remoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote",
		Short: "Read resources from, and call tools on, a remote Ariadne MCP server",
	}
	cmd.AddCommand(
		remoteReadCmd(),
		remoteCallCmd(),
		remoteStartCmd(),
		remoteCancelCmd(),
	)
	return cmd
}

func remoteReadCmd() *cobra.Command {
	var endpoint string

	cmd := &cobra.Command{
		Use:   "read",
		Short: "Read a resource from a remote Ariadne MCP server",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("usage: ariadne remote read <uri>")
			}
			session, err := connectMCP(cmd.Context(), endpoint)
			if err != nil {
				return err
			}
			defer session.Close()

			result, err := session.ReadResource(cmd.Context(), &mcp.ReadResourceParams{URI: args[0]})
			if err != nil {
				return err
			}
			for _, content := range result.Contents {
				if strings.TrimSpace(content.Text) != "" {
					fmt.Fprintln(cmd.OutOrStdout(), content.Text)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "http://127.0.0.1:7618/mcp", "streamable HTTP MCP endpoint")
	return cmd
}

func remoteCallCmd() *cobra.Command {
	var endpoint string
	var argsJSON string

	cmd := &cobra.Command{
		Use:   "call",
		Short: "Call a tool on a remote Ariadne MCP server",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("usage: ariadne remote call <tool>")
			}
			payload := map[string]any{}
			if strings.TrimSpace(argsJSON) != "" {
				if err := json.Unmarshal([]byte(argsJSON), &payload); err != nil {
					return fmt.Errorf("decode --args JSON: %w", err)
				}
			}
			return callRemoteTool(cmd.Context(), cmd.OutOrStdout(), endpoint, args[0], payload)
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "http://127.0.0.1:7618/mcp", "streamable HTTP MCP endpoint")
	cmd.Flags().StringVar(&argsJSON, "args", "", "tool arguments as JSON object")
	return cmd
}

func remoteStartCmd() *cobra.Command {
	var endpoint string
	var title string
	var description string
	var taskID string
	var provider string
	var persona string
	var routing string
	var sourceURL string
	var labels []string

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Convenience wrapper for the remote start_run tool",
		RunE: func(cmd *cobra.Command, _ []string) error {
			payload := map[string]any{
				"title":       title,
				"description": description,
			}
			if strings.TrimSpace(taskID) != "" {
				payload["task_id"] = taskID
			}
			if strings.TrimSpace(provider) != "" {
				payload["provider"] = provider
			}
			if strings.TrimSpace(persona) != "" {
				payload["persona"] = persona
			}
			if strings.TrimSpace(routing) != "" {
				payload["routing"] = routing
			}
			if strings.TrimSpace(sourceURL) != "" {
				payload["source_url"] = sourceURL
			}
			if len(labels) > 0 {
				payload["labels"] = labels
			}
			return callRemoteTool(cmd.Context(), cmd.OutOrStdout(), endpoint, "start_run", payload)
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "http://127.0.0.1:7618/mcp", "streamable HTTP MCP endpoint")
	cmd.Flags().StringVar(&title, "title", "", "task title")
	cmd.Flags().StringVar(&description, "description", "", "task description/body")
	cmd.Flags().StringVar(&taskID, "task-id", "", "optional task identifier")
	cmd.Flags().StringVar(&provider, "provider", "", "pin a provider for this run")
	cmd.Flags().StringVar(&persona, "persona", "", "pin a persona for this run")
	cmd.Flags().StringVar(&routing, "routing", "", "override routing strategy")
	cmd.Flags().StringVar(&sourceURL, "source-url", "", "optional source URL")
	cmd.Flags().StringSliceVar(&labels, "labels", nil, "task labels")
	_ = cmd.MarkFlagRequired("title")
	_ = cmd.MarkFlagRequired("description")
	return cmd
}

func remoteCancelCmd() *cobra.Command {
	var endpoint string
	var runID string

	cmd := &cobra.Command{
		Use:   "cancel",
		Short: "Convenience wrapper for the remote cancel_run tool",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return callRemoteTool(cmd.Context(), cmd.OutOrStdout(), endpoint, "cancel_run", map[string]any{
				"run_id": runID,
			})
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "http://127.0.0.1:7618/mcp", "streamable HTTP MCP endpoint")
	cmd.Flags().StringVar(&runID, "run-id", "", "run ID to cancel")
	_ = cmd.MarkFlagRequired("run-id")
	return cmd
}

func connectMCP(ctx context.Context, endpoint string) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "ariadne-remote", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to MCP endpoint %s: %w", endpoint, err)
	}
	return session, nil
}

func callRemoteTool(ctx context.Context, out io.Writer, endpoint, tool string, payload map[string]any) error {
	session, err := connectMCP(ctx, endpoint)
	if err != nil {
		return err
	}
	defer session.Close()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: payload})
	if err != nil {
		return err
	}

	if result.StructuredContent != nil {
		data, err := json.MarshalIndent(result.StructuredContent, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(out, string(data))
		return nil
	}

	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			fmt.Fprintln(out, text.Text)
		}
	}
	return nil
}
