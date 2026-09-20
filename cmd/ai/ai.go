/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package ai is the CLI half of Adhar's agentic layer (ADR-0024, ADR-0025).
//
// WHAT THIS TALKS TO. Nothing here embeds a provider SDK or a model key. Every
// completion goes to the platform's own AI data plane — agentgateway, Service
// `adhar-ai-gateway` on :8080, publicly `https://ai.<host>/v1` — which picks the
// provider from the MODEL NAME (`claude-*` → Anthropic, `gpt-*`/`o[1-9]-*` →
// OpenAI, `local/*` → the in-cluster vLLM behind llm-d), holds the one key
// server-side, meters tokens against the platform budget and applies the prompt
// guardrails. That is the whole point of routing the CLI through the cluster
// instead of straight to a vendor: one key, one audit trail, one budget, and a
// laptop that never holds a credential.
//
// WHY THERE IS AN AGENT LOOP IN THE CLI. The Python agent runtime
// (adhar-io/adhar-ai) serves the Console chat and the event-driven operators.
// The CLI does not proxy to it, because a developer's questions are about the
// cluster they are pointed at *right now* and the answers come from resources
// their own kubeconfig can already read. So `adhar ai agent` runs the
// tool-calling loop locally over read-only platform tools (tools.go) under the
// SAME staged-autonomy ladder the runtime uses, read from the same
// `adhar-ai-config` ConfigMap. Authority only ever narrows: the ConfigMap sets
// the ceiling, --autonomy can lower it, and no built-in tool can mutate the
// cluster at all — the one write tool opens a reviewable proposal.
package ai

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// Shared flags. Every subcommand reaches the platform the same way, so the
// connection knobs live on the parent as persistent flags.
var (
	flagNamespace string
	flagEndpoint  string
	flagModel     string
	flagInsecure  bool
	flagTimeout   time.Duration
	flagJSON      bool
)

// AICmd is `adhar ai`.
var AICmd = &cobra.Command{
	Use:     "ai",
	Aliases: []string{"agent"},
	Short:   "🤖 Ask, investigate and act through the platform's own AI data plane",
	Long: `Adhar AI turns the platform into something you can ask questions of. Completions run through the cluster's AI data plane (agentgateway), which holds the single provider key server-side, routes to Anthropic, OpenAI or the in-cluster vLLM by model name, and meters every request against the platform budget — so your laptop never holds a credential and every call is audited.

Beyond chat, ` + "`adhar ai agent`" + ` runs a tool-calling loop over read-only
platform tools: it reads resources, pods, logs, events, Argo CD app health and
the package catalogue through YOUR kubeconfig, and reasons over what it finds.
It runs on the staged-autonomy ladder from the cluster's own adhar-ai-config
(read-only → suggest → approve-to-apply → scoped): no built-in tool can mutate
the cluster, and changes are only ever offered as a reviewable proposal you push
yourself.

Getting started:
  adhar auth login <you>          # the data plane requires a platform token
  adhar ai key set                # give the platform its provider key (once)
  adhar ai status                 # what is installed, keyed and reachable
  adhar ai ask "what is OutOfSync and why?"
  adhar ai diagnose keycloak      # agentic triage of one app`,
	Example: `  # One-shot question, answered by the platform's configured model
  adhar ai ask "which packages are unhealthy and what do they have in common?"

  # Interactive session with the cluster in context
  adhar ai chat

  # Agentic triage: the loop reads pods, logs and events on its own
  adhar ai diagnose metabase

  # Explain a live resource
  adhar ai explain deployment/adhar-console

  # What can the agent actually call?
  adhar ai tools

  # Route to a specific model (in-cluster inference needs no external key)
  adhar ai ask "summarise the platform" --model local/Qwen/Qwen2.5-0.5B-Instruct`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// A bare `adhar ai` is a question about the AI stack itself, not a chat
		// prompt: answering with the status panel tells someone who just enabled
		// the packages whether it is ready, which is what they came to find out.
		return runStatus(cmd, args)
	},
	SilenceUsage: true,
}

func init() {
	p := AICmd.PersistentFlags()
	p.StringVarP(&flagNamespace, "namespace", "n", "adhar-system", "Namespace the AI data plane runs in")
	p.StringVar(&flagEndpoint, "endpoint", "", "Base URL of the AI data plane (default: port-forward to the in-cluster gateway)")
	p.StringVar(&flagModel, "model", "", "Model to route to (default: the platform's configured model)")
	p.BoolVar(&flagInsecure, "insecure", false, "Skip TLS verification (for the platform's self-signed development certificate)")
	p.DurationVar(&flagTimeout, "timeout", 3*time.Minute, "Timeout for a single model call")
	p.BoolVar(&flagJSON, "json", false, "Machine-readable output where a command supports it")

	AICmd.AddCommand(
		statusCmd,
		modelsCmd,
		askCmd,
		chatCmd,
		agentCmd,
		diagnoseCmd,
		explainCmd,
		toolsCmd,
		mcpCmd,
		keyCmd,
		autonomyCmd,
		budgetCmd,
	)
}

// hint prints a short, actionable next step. Used wherever a command finds the
// stack half-installed, which on a platform where the AI packages are disabled
// by default is the common case rather than an error.
func hint(format string, a ...interface{}) {
	fmt.Printf("  → "+format+"\n", a...)
}
