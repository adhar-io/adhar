/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
)

// The federated MCP endpoint.
//
// agentgateway multiplexes the seven per-domain MCP servers behind ONE
// StreamableHTTP endpoint, validates the Keycloak JWT once and authorizes per
// tool. That is the same surface the Console chat and any external agent (an IDE,
// Claude Code) use, so a tool listed here behaves identically wherever it is
// called from — which is the point of reading it from the gateway rather than
// talking to the seven servers directly.
//
// Names are prefixed by the gateway with their domain (`cluster_…`), so a tool
// mounted into the CLI loop keeps the name it has everywhere else.

const mcpProtocolVersion = "2025-06-18"

type mcpClient struct {
	base string // the gateway base URL
	path string // /mcp
	http *http.Client
	tok  string
	id   int
}

func newMCPClient(c *gwClient) *mcpClient {
	return &mcpClient{base: c.base, path: "/mcp", http: c.http, tok: c.token}
}

type rpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (m *mcpClient) call(ctx context.Context, method string, params interface{}, out interface{}) error {
	m.id++
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: m.id, Method: method, Params: params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.base+m.path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// StreamableHTTP servers may answer either shape; accept both so a compliant
	// server is never rejected over content negotiation.
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
	if m.tok != "" {
		req.Header.Set("Authorization", "Bearer "+m.tok)
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return fmt.Errorf("calling the federated MCP endpoint: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("the gateway has no /mcp route — the adhar-ai MCP servers are not installed")
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("MCP endpoint returned HTTP %d: %s", resp.StatusCode, truncate(strings.TrimSpace(string(raw)), 300))
	}
	payload := extractSSEData(raw)
	var rr rpcResponse
	if err := json.Unmarshal(payload, &rr); err != nil {
		return fmt.Errorf("unreadable MCP response: %w (%s)", err, truncate(string(payload), 200))
	}
	if rr.Error != nil {
		return fmt.Errorf("MCP error %d: %s", rr.Error.Code, rr.Error.Message)
	}
	if out != nil && len(rr.Result) > 0 {
		return json.Unmarshal(rr.Result, out)
	}
	return nil
}

// extractSSEData unwraps a single-event SSE body. A StreamableHTTP server is
// allowed to answer a request/response call as one `data:` frame, and parsing that
// as bare JSON fails — which looked like a protocol error when it was a framing
// one.
func extractSSEData(raw []byte) []byte {
	s := string(raw)
	if !strings.Contains(s, "data:") {
		return raw
	}
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "data:") {
			b.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if b.Len() == 0 {
		return raw
	}
	return []byte(b.String())
}

func (m *mcpClient) initialize(ctx context.Context) error {
	return m.call(ctx, "initialize", map[string]interface{}{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]interface{}{"name": "adhar-cli", "version": "0.1"},
	}, nil)
}

type mcpTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

func (m *mcpClient) listTools(ctx context.Context) ([]mcpTool, error) {
	var out struct {
		Tools []mcpTool `json:"tools"`
	}
	if err := m.call(ctx, "tools/list", map[string]interface{}{}, &out); err != nil {
		return nil, err
	}
	return out.Tools, nil
}

func (m *mcpClient) callTool(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := m.call(ctx, "tools/call", map[string]interface{}{"name": name, "arguments": args}, &out); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, c := range out.Content {
		b.WriteString(c.Text)
		b.WriteString("\n")
	}
	if out.IsError {
		return b.String(), fmt.Errorf("the tool reported an error")
	}
	return b.String(), nil
}

// loadMCPTools mounts the federated tools into the CLI loop.
//
// A federated tool is treated as a WRITE tool whenever its name suggests it
// changes something, because the CLI cannot introspect what a remote tool does and
// the safe assumption is the conservative one. The platform's own servers only
// ever open pull requests, so this classification decides whether the CLI pauses
// for confirmation — not whether the cluster can be mutated behind your back.
func loadMCPTools(ctx context.Context, p *platform, c *gwClient) ([]tool, error) {
	m := newMCPClient(c)
	if err := m.initialize(ctx); err != nil {
		return nil, err
	}
	remote, err := m.listTools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]tool, 0, len(remote))
	for _, rt := range remote {
		rt := rt
		params := rt.InputSchema
		if params == nil {
			params = objSchema(nil, map[string]interface{}{})
		}
		out = append(out, tool{
			name:        rt.Name,
			description: rt.Description + " (federated MCP tool)",
			params:      params,
			write:       looksLikeWrite(rt.Name),
			run: func(ctx context.Context, _ *toolbox, args map[string]interface{}) (string, error) {
				return m.callTool(ctx, rt.Name, args)
			},
		})
	}
	return out, nil
}

// looksLikeWrite is a deliberately broad heuristic: over-classifying a read as a
// write costs one confirmation prompt, while the reverse would skip one.
func looksLikeWrite(name string) bool {
	n := strings.ToLower(name)
	for _, verb := range []string{"propose", "create", "apply", "write", "update", "patch", "delete", "open_pr", "pull_request", "commit", "scale", "restart", "sync", "rotate", "provision"} {
		if strings.Contains(n, verb) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// `adhar ai mcp`
// ---------------------------------------------------------------------------

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Inspect and call the platform's federated MCP tools",
	Long: `Work with the platform's federated MCP endpoint.

Adhar exposes its seven per-domain tool servers (cluster, gitops, provision,
observability, security, cost, catalog) as ONE MCP endpoint on agentgateway,
which validates your platform token once and authorizes per tool. Any MCP client
— an IDE, Claude Code, a ChatOps bot — drives Adhar through the identical governed
tools with one URL and one token; these subcommands are for checking that surface
from a terminal.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return cmd.Help()
	},
}

var mcpListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls", "tools"},
	Short:   "List the tools the federated MCP endpoint offers",
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := ensureContext(cmd.Context())
		p, err := newPlatform()
		if err != nil {
			return err
		}
		c, err := dial(ctx, p)
		if err != nil {
			return err
		}
		defer c.Close()
		m := newMCPClient(c)
		if err := m.initialize(ctx); err != nil {
			return err
		}
		tools, err := m.listTools(ctx)
		if err != nil {
			return err
		}
		if len(tools) == 0 {
			fmt.Println("\n  the endpoint offers no tools — the adhar-ai MCP servers may not be running")
			fmt.Println()
			return nil
		}
		fmt.Println()
		fmt.Println(helpers.SectionHeading("🔌", fmt.Sprintf("Federated MCP tools (%d)", len(tools))))
		fmt.Println()
		t := helpers.NewTable("TOOL", "KIND", "DESCRIPTION")
		for _, tl := range tools {
			kind := helpers.StateReady("read")
			if looksLikeWrite(tl.Name) {
				kind = helpers.StateDegraded("write (PR)")
			}
			t.Row(tl.Name, kind, firstSentence(tl.Description))
		}
		fmt.Println(t.Render())
		fmt.Println()
		hint("Use them in a run:  adhar ai agent \"…\" --mcp")
		fmt.Println()
		return nil
	},
	SilenceUsage: true,
}

var mcpCallArgs []string

var mcpCallCmd = &cobra.Command{
	Use:   "call <tool>",
	Short: "Call one federated MCP tool directly",
	Long: `Call one MCP tool and print its result.

Useful for checking that a tool works, and what it returns, without a model in the
loop. Arguments are key=value pairs; a value that parses as JSON is sent as JSON,
so numbers, booleans and objects all work.`,
	Example: `  adhar ai mcp call cluster_list_pods --arg namespace=adhar-system
  adhar ai mcp call observability_promql --arg 'query=up{job="kubelet"}'`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := ensureContext(cmd.Context())
		p, err := newPlatform()
		if err != nil {
			return err
		}
		c, err := dial(ctx, p)
		if err != nil {
			return err
		}
		defer c.Close()
		m := newMCPClient(c)
		if err := m.initialize(ctx); err != nil {
			return err
		}
		params, err := parseArgs(mcpCallArgs)
		if err != nil {
			return err
		}
		out, err := m.callTool(ctx, args[0], params)
		if out != "" {
			fmt.Println(out)
		}
		return err
	},
	SilenceUsage: true,
}

// parseArgs turns --arg k=v pairs into tool arguments, treating a JSON-parseable
// value as JSON so typed parameters are reachable from a shell.
func parseArgs(pairs []string) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			return nil, fmt.Errorf("--arg expects key=value, got %q", p)
		}
		var typed interface{}
		if err := json.Unmarshal([]byte(v), &typed); err == nil {
			out[k] = typed
			continue
		}
		out[k] = v
	}
	return out, nil
}

func init() {
	mcpCallCmd.Flags().StringArrayVar(&mcpCallArgs, "arg", nil, "Tool argument as key=value (repeatable)")
	mcpCmd.AddCommand(mcpListCmd, mcpCallCmd)
}
