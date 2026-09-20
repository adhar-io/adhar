/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:     "status",
	Aliases: []string{"st"},
	Short:   "Show what the AI stack has installed, keyed and reachable",
	Long: `Show the state of the platform's AI stack.

Reports each part separately — the data plane (agentgateway), the provider key,
the agent runtime, the federated MCP tool servers and in-cluster inference —
because they fail independently and each has a different remedy. The AI packages
are disabled by default on every profile, so "not installed" is an ordinary
answer here, not an error.`,
	RunE:         runStatus,
	SilenceUsage: true,
}

// component is one line of the status panel.
type component struct {
	Name   string `json:"name"`
	State  string `json:"state"` // ready | degraded | absent
	Detail string `json:"detail"`
}

func runStatus(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	p, err := newPlatform()
	if err != nil {
		return err
	}

	var comps []component
	add := func(name, state, detail string) { comps = append(comps, component{name, state, detail}) }

	// 1. The AI data plane. The gateway Service is provisioned by the
	// agentgateway controller from the Gateway resource, so a missing Service
	// with a present controller means the Gateway is not Accepted yet.
	ctrlReady, ctrlWant, ctrlFound := p.deploymentReady(ctx, "agentgateway")
	switch {
	case !ctrlFound:
		add("AI data plane (agentgateway)", "absent", "package not installed")
	case ctrlReady < ctrlWant:
		add("AI data plane (agentgateway)", "degraded", fmt.Sprintf("controller %d/%d ready", ctrlReady, ctrlWant))
	case !p.serviceExists(ctx, gatewayService):
		add("AI data plane (agentgateway)", "degraded", "controller up, Gateway adhar-ai-gateway not provisioned yet")
	default:
		add("AI data plane (agentgateway)", "ready", "Service "+gatewayService+":8080 · /v1 routes by model name")
	}

	// 2. The provider key. This is the single switch between "installed" and
	// "agentic", so it gets its own line and never prints a key.
	cfg, err := p.readLLMConfig(ctx)
	if err != nil {
		add("Provider key", "degraded", "cannot read Secret "+llmSecret+": "+err.Error())
		cfg = &llmConfig{}
	} else {
		switch {
		case !cfg.SecretPresent:
			add("Provider key", "absent", "Secret "+llmSecret+" not projected — run `adhar ai key set`")
		case !cfg.Keyed && cfg.Provider == "local":
			add("Provider key", "ready", "provider=local (in-cluster inference needs no key)")
		case !cfg.Keyed:
			add("Provider key", "degraded", "Secret present but every key slot is empty — run `adhar ai key set`")
		default:
			slots := strings.Join(cfg.KeyedProviders, ", ")
			if slots == "" {
				slots = cfg.Provider
			}
			add("Provider key", "ready", fmt.Sprintf("provider=%s model=%s keyed=[%s]", orDash(cfg.Provider), orDash(cfg.Model), slots))
		}
	}

	// 3. The agent runtime (the Python service behind the Console chat). The CLI
	// does not need it — `adhar ai agent` runs its own loop — so an absent
	// runtime is reported without alarm.
	rtReady, rtWant, rtFound := p.deploymentReady(ctx, runtimeService)
	switch {
	case !rtFound:
		add("Agent runtime (Console chat)", "absent", "not installed — the CLI agent loop does not need it")
	case rtReady < rtWant:
		add("Agent runtime (Console chat)", "degraded", fmt.Sprintf("%d/%d ready", rtReady, rtWant))
	default:
		add("Agent runtime (Console chat)", "ready", fmt.Sprintf("%d/%d ready", rtReady, rtWant))
	}

	// 4. The federated MCP tool servers.
	var mcpUp, mcpTotal int
	for _, d := range mcpDomains {
		ready, want, found := p.deploymentReady(ctx, "adhar-ai-mcp-"+d)
		if !found {
			continue
		}
		mcpTotal++
		if ready >= want && ready > 0 {
			mcpUp++
		}
	}
	switch {
	case mcpTotal == 0:
		add("MCP tool servers", "absent", fmt.Sprintf("none of the %d per-domain servers installed", len(mcpDomains)))
	case mcpUp < mcpTotal:
		add("MCP tool servers", "degraded", fmt.Sprintf("%d/%d ready", mcpUp, mcpTotal))
	default:
		add("MCP tool servers", "ready", fmt.Sprintf("%d/%d ready · federated at /mcp", mcpUp, mcpTotal))
	}

	// 5. In-cluster inference, which is what makes `local/*` models answerable
	// with no external key at all.
	llmdReady, _, llmdFound := p.deploymentReady(ctx, "llm-d-inference-gateway")
	vllmReady, _, vllmFound := p.deploymentReady(ctx, "vllm")
	switch {
	case llmdFound && llmdReady > 0:
		add("In-cluster inference", "ready", "llm-d serving local/* models")
	case vllmFound && vllmReady > 0:
		add("In-cluster inference", "ready", "vLLM serving local/* models")
	case llmdFound || vllmFound:
		add("In-cluster inference", "degraded", "installed but no ready replica")
	default:
		add("In-cluster inference", "absent", "no local/* models; completions go to the configured provider")
	}

	// 6. Autonomy ceiling, read from the cluster's own config so the CLI reports
	// the authority it will actually run with rather than its own default.
	ac := p.readAgentConfig(ctx)
	add("Autonomy ceiling", stateFor(ac.source != ""), fmt.Sprintf("%s (maxSteps=%d) · %s", ac.Level, ac.MaxSteps, ac.source))

	// 7. Identity. Every call into the data plane carries the platform token.
	user, issuer := currentIdentity(ctx)
	if user == "" {
		add("Platform session", "absent", "not logged in — run `adhar auth login <username>`")
	} else {
		add("Platform session", "ready", user+" @ "+issuer)
	}

	if flagJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]interface{}{"components": comps})
	}

	fmt.Println()
	fmt.Println(helpers.SectionHeading("🤖", "Adhar AI"))
	fmt.Println()
	t := helpers.NewTable("COMPONENT", "STATE", "DETAIL")
	for _, c := range comps {
		t.Row(c.Name, renderState(c.State), c.Detail)
	}
	fmt.Println(t.Render())

	// Close with the one next step that matters, in dependency order: a stack
	// that is not installed, then not keyed, then not logged in, then ready.
	fmt.Println()
	switch {
	case comps[0].State == "absent":
		hint("Enable the AI stack: set ai/agentgateway (and ai/adhar-ai) enabled in")
		hint("platform/stack/environments/<env>/config.yaml, then run `adhar upgrade`.")
	case !cfg.Keyed && cfg.Provider != "local":
		hint("Give the platform its key:  adhar ai key set")
	case user == "":
		hint("Log in so the data plane accepts your calls:  adhar auth login <username>")
	default:
		hint("Ask it something:  adhar ai ask \"which apps are unhealthy and why?\"")
		hint("Or investigate:     adhar ai diagnose <app>")
	}
	fmt.Println()
	return nil
}

// mcpDomains are the seven per-domain MCP servers the adhar-ai package installs.
var mcpDomains = []string{"cluster", "gitops", "provision", "observability", "security", "cost", "catalog"}

func renderState(s string) string {
	switch s {
	case "ready":
		return helpers.StateReady("ready")
	case "degraded":
		return helpers.StateDegraded("degraded")
	case "absent":
		return helpers.StateDisabled("absent")
	}
	return helpers.StateUnknown(s)
}

func stateFor(ok bool) string {
	if ok {
		return "ready"
	}
	return "absent"
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// budgetCmd reports the token budget the data plane meters against. The numbers
// are the platform's declared ceilings; live consumption is a Prometheus series
// that the observability stack owns, so this points at it rather than guessing.
var budgetCmd = &cobra.Command{
	Use:   "budget",
	Short: "Show the AI token and tool-call budgets the platform enforces",
	Long: `Show the budgets the AI data plane meters every request against.

The ceilings live with the provider configuration in the secrets backend, so they
are shown here from the projected Secret. Live consumption is exported by
agentgateway as Prometheus series and is best read in Grafana, which this points
you to rather than sampling one scrape here and calling it a total.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		p, err := newPlatform()
		if err != nil {
			return err
		}
		cfg, err := p.readLLMConfig(ctx)
		if err != nil {
			return err
		}
		ac := p.readAgentConfig(ctx)
		if flagJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(map[string]interface{}{
				"perUserDailyTokens": cfg.DailyTokens,
				"perOpMaxToolCalls":  cfg.MaxToolCalls,
				"agentMaxSteps":      ac.MaxSteps,
				"autonomy":           ac.Level,
			})
		}
		fmt.Println()
		fmt.Println(helpers.SectionHeading("💳", "AI budgets"))
		fmt.Println()
		t := helpers.NewTable("BUDGET", "CEILING", "ENFORCED BY")
		t.Row("Tokens per user per day", orDash(cfg.DailyTokens), "agentgateway (429 when exhausted)")
		t.Row("Tool calls per operation", orDash(cfg.MaxToolCalls), "agentgateway + agent runtime")
		t.Row("Agent steps per run", fmt.Sprintf("%d", ac.MaxSteps), "adhar-ai-config (CLI honours it)")
		t.Row("Autonomy ceiling", ac.Level, ac.source)
		fmt.Println(t.Render())
		fmt.Println()
		hint("Live consumption: the \"Adhar AI\" Grafana dashboard (agentgateway token metrics).")
		fmt.Println()
		return nil
	},
	SilenceUsage: true,
}

// currentIdentity is a thin wrapper so status does not import cmd/auth types.
func currentIdentity(ctx context.Context) (string, string) {
	return identityFn(ctx)
}
