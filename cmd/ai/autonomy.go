/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package ai

import (
	"context"
	"fmt"
	"strings"

	"adhar-io/adhar/cmd/auth"
	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// The staged-autonomy ladder, defined by ADR-0024 and configured in the
// cluster's adhar-ai-config ConfigMap. The CLI implements the same four rungs so
// that a run from a terminal has exactly the authority the platform grants the
// runtime — no more, and observably less when asked.
//
// Ordered weakest first: the ordering IS the semantics, because narrowing is the
// only permitted direction.
const (
	autonomyReadOnly = "read-only"
	autonomySuggest  = "suggest"
	autonomyApprove  = "approve-to-apply"
	autonomyScoped   = "scoped"
)

var autonomyLadder = []string{autonomyReadOnly, autonomySuggest, autonomyApprove, autonomyScoped}

// rank returns a comparable strength for a rung, and -1 for anything unknown.
// Unknown must not silently become the default: a typo'd --autonomy that fell
// back to `suggest` would hand out authority the operator did not ask for.
func rank(level string) int {
	for i, l := range autonomyLadder {
		if l == level {
			return i
		}
	}
	return -1
}

// agentConfig is the slice of adhar-ai-config the CLI honours.
type agentConfig struct {
	Level        string
	MaxSteps     int
	MaxToolCalls int
	AllowedRepos []string
	AllowedPaths []string
	ScopedRepos  []string
	ScopedPaths  []string
	source       string // where the values came from, for honest reporting
}

// defaultAgentConfig matches the ConfigMap's shipped defaults, so a cluster
// without the adhar-ai package behaves like one with it rather than more freely.
func defaultAgentConfig() agentConfig {
	return agentConfig{
		Level:        autonomySuggest,
		MaxSteps:     12,
		MaxToolCalls: 40,
		AllowedRepos: []string{"packages", "environments"},
		AllowedPaths: []string{"packages/", "environments/"},
		source:       "CLI default (adhar-ai-config not found)",
	}
}

// readAgentConfig loads the autonomy ceiling from the cluster.
//
// A parse failure falls back to the shipped defaults rather than erroring: the
// point of reading this is to be no more permissive than the platform, and a
// config the CLI cannot read is not a reason to run with more authority.
func (p *platform) readAgentConfig(ctx context.Context) agentConfig {
	out := defaultAgentConfig()
	cm, err := p.clients.CoreV1().ConfigMaps(p.ns).Get(ctx, agentConfigMap, metav1.GetOptions{})
	if err != nil {
		return out
	}
	raw, ok := cm.Data["config.yaml"]
	if !ok {
		return out
	}
	var parsed struct {
		Autonomy struct {
			Default string `json:"default"`
		} `json:"autonomy"`
		Limits struct {
			MaxSteps          int `json:"maxSteps"`
			MaxToolCallsPerOp int `json:"maxToolCallsPerOp"`
		} `json:"limits"`
		WritePolicy struct {
			AllowedRepos        []string `json:"allowedRepos"`
			AllowedPathPrefixes []string `json:"allowedPathPrefixes"`
			Scoped              struct {
				AllowedRepos        []string `json:"allowedRepos"`
				AllowedPathPrefixes []string `json:"allowedPathPrefixes"`
			} `json:"scoped"`
		} `json:"writePolicy"`
	}
	if err := yaml.Unmarshal([]byte(raw), &parsed); err != nil {
		out.source = "CLI default (adhar-ai-config is unparseable)"
		return out
	}
	out.source = "adhar-ai-config in " + p.ns
	if rank(parsed.Autonomy.Default) >= 0 {
		out.Level = parsed.Autonomy.Default
	}
	if parsed.Limits.MaxSteps > 0 {
		out.MaxSteps = parsed.Limits.MaxSteps
	}
	if parsed.Limits.MaxToolCallsPerOp > 0 {
		out.MaxToolCalls = parsed.Limits.MaxToolCallsPerOp
	}
	if len(parsed.WritePolicy.AllowedRepos) > 0 {
		out.AllowedRepos = parsed.WritePolicy.AllowedRepos
	}
	if len(parsed.WritePolicy.AllowedPathPrefixes) > 0 {
		out.AllowedPaths = parsed.WritePolicy.AllowedPathPrefixes
	}
	out.ScopedRepos = parsed.WritePolicy.Scoped.AllowedRepos
	out.ScopedPaths = parsed.WritePolicy.Scoped.AllowedPathPrefixes
	return out
}

// effectiveAutonomy applies a requested rung to a ceiling. Requests may only
// narrow; a request above the ceiling is refused loudly rather than clamped
// silently, because someone who typed `--autonomy scoped` and got `suggest`
// would go on to believe the run was unattended.
func effectiveAutonomy(ceiling, requested string) (string, error) {
	if requested == "" {
		return ceiling, nil
	}
	r := rank(requested)
	if r < 0 {
		return "", fmt.Errorf("unknown autonomy level %q — expected one of: %s", requested, strings.Join(autonomyLadder, ", "))
	}
	if r > rank(ceiling) {
		return "", fmt.Errorf("autonomy %q is above the platform ceiling %q set by %s; authority can only be narrowed here, raise the ceiling in the ConfigMap through GitOps if that is what you want", requested, ceiling, agentConfigMap)
	}
	return requested, nil
}

// writesAllowed reports whether the write tool is offered at this rung, and the
// path/repo allow-list it must obey. At `scoped` the NARROWER list applies and is
// empty by default, which is a refusal with an explanation rather than a silent
// widening.
func (a agentConfig) writesAllowed(level string) (bool, []string, []string, string) {
	switch level {
	case autonomyReadOnly:
		return false, nil, nil, "autonomy is read-only: write tools are not offered"
	case autonomyScoped:
		if len(a.ScopedRepos) == 0 && len(a.ScopedPaths) == 0 {
			return false, nil, nil, "autonomy `scoped` runs unattended and its narrower allow-list (writePolicy.scoped) is empty, so no write is permitted — enumerate writePolicy.scoped.allowedRepos/allowedPathPrefixes first"
		}
		return true, a.ScopedRepos, a.ScopedPaths, ""
	default:
		return true, a.AllowedRepos, a.AllowedPaths, ""
	}
}

var autonomyCmd = &cobra.Command{
	Use:   "autonomy",
	Short: "Show the staged-autonomy ladder and the ceiling this platform grants",
	Long: `Show the autonomy ceiling the platform grants agentic runs.

Adhar's agent authority is a ladder (ADR-0024), and every rung differs in a way
you can observe:

  read-only         write tools are not offered, and are refused if called anyway
  suggest           write tools allowed; the first proposal ends the run, so a
                    human reads one change rather than a chain
  approve-to-apply  the run continues after a proposal, so the agent can verify
                    its own work; a human still merges every one
  scoped            runs unattended, and is therefore confined to the NARROWER
                    writePolicy.scoped allow-list — empty by default, so this
                    rung permits no write at all until an operator enumerates it

The ceiling lives in the cluster (ConfigMap adhar-ai-config, reconciled by
ArgoCD), so raising it is a reviewed Git change, not a CLI flag. --autonomy on
` + "`adhar ai agent`" + ` can only narrow it.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		p, err := newPlatform()
		if err != nil {
			return err
		}
		ac := p.readAgentConfig(ctx)
		fmt.Println()
		fmt.Println(helpers.SectionHeading("🪜", "Agent autonomy"))
		fmt.Println()
		t := helpers.NewTable("RUNG", "GRANTED", "WHAT IT MEANS")
		for _, l := range autonomyLadder {
			granted := helpers.StateDisabled("above ceiling")
			if rank(l) <= rank(ac.Level) {
				granted = helpers.StateReady("available")
			}
			if l == ac.Level {
				granted = helpers.StateReady("default")
			}
			t.Row(l, granted, autonomyMeaning[l])
		}
		fmt.Println(t.Render())
		fmt.Println()
		fmt.Printf("  ceiling %s · source %s · maxSteps %d · maxToolCalls %d\n", ac.Level, ac.source, ac.MaxSteps, ac.MaxToolCalls)
		if ok, repos, paths, why := ac.writesAllowed(ac.Level); ok {
			fmt.Printf("  proposals may touch repos %v under %v\n", repos, paths)
		} else {
			fmt.Printf("  no writes at the default rung: %s\n", why)
		}
		fmt.Println()
		hint("Raise or lower it through GitOps: ai/adhar-ai/manifests/agent-runtime.yaml")
		fmt.Println()
		return nil
	},
	SilenceUsage: true,
}

var autonomyMeaning = map[string]string{
	autonomyReadOnly: "reads only; no write tool is even offered",
	autonomySuggest:  "one proposal, then the run stops for a human",
	autonomyApprove:  "keeps going after a proposal; a human still merges",
	autonomyScoped:   "unattended, inside writePolicy.scoped only",
}

// identityFn is indirected so tests can run status without a session file.
var identityFn = func(ctx context.Context) (string, string) {
	return auth.PlatformIdentity(ctx)
}
