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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var modelsCmd = &cobra.Command{
	Use:     "models",
	Aliases: []string{"model"},
	Short:   "List the models the AI data plane will route to",
	Long: `List what --model may be set to.

The data plane routes by model NAME, not by endpoint: ` + "`claude-*`" + ` and
` + "`anthropic/*`" + ` go to Anthropic, ` + "`gpt-*`" + ` / ` + "`o1-*`" + ` / ` + "`openai/*`" + ` to OpenAI (which is
also how an OpenAI-compatible provider such as OpenRouter is reached), and
` + "`local/*`" + ` / ` + "`vllm/*`" + ` to in-cluster inference that needs no external key at all.
That routing table is the honest answer to "what can I use", so it is shown
alongside whatever the gateway reports and the models actually being served
in-cluster.`,
	RunE:         runModels,
	SilenceUsage: true,
}

// modelRoute is one row of agentgateway's llm-routes table.
type modelRoute struct {
	Pattern  string `json:"pattern"`
	Backend  string `json:"backend"`
	KeySlot  string `json:"keySlot"`
	Provider string `json:"provider"`
}

// routingTable mirrors ai/agentgateway/manifests/llm-routes.yaml. It is declared
// here rather than derived from the cluster because the HTTPRoute expresses it as
// regular expressions on a header the gateway extracts from the request body —
// readable to a proxy, not to a user asking "what do I type?".
var routingTable = []modelRoute{
	{"claude-* · anthropic/*", "llm-anthropic", "anthropicApiKey", "anthropic"},
	{"gpt-* · o1-* · chatgpt-* · openai/*", "llm-openai", "openaiApiKey", "openai (or any OpenAI-compatible endpoint)"},
	{"local/* · vllm/*", "llm-vllm", "— none —", "in-cluster inference (llm-d / vLLM)"},
	{"anything else", "llm-openai", "openaiApiKey", "fallback route"},
}

func runModels(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	p, err := newPlatform()
	if err != nil {
		return err
	}
	cfg, _ := p.readLLMConfig(ctx)
	served := p.servedModels(ctx)

	// Ask the gateway what it will serve. This is best-effort on purpose: a
	// gateway that does not implement /v1/models is not broken, and the routing
	// table below is still the truth about what will route.
	var advertised []string
	if c, derr := dial(ctx, p); derr == nil {
		defer c.Close()
		if ids, merr := c.models(ctx); merr == nil {
			advertised = ids
		}
	}

	if flagJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]interface{}{
			"default":    cfg.Model,
			"routes":     routingTable,
			"advertised": advertised,
			"inCluster":  served,
			"keyed":      cfg.KeyedProviders,
		})
	}

	fmt.Println()
	fmt.Println(helpers.SectionHeading("🧠", "Models"))
	fmt.Println()
	t := helpers.NewTable("MODEL NAME", "ROUTES TO", "NEEDS A KEY IN")
	for _, r := range routingTable {
		t.Row(r.Pattern, r.Provider, r.KeySlot)
	}
	fmt.Println(t.Render())

	if len(served) > 0 {
		fmt.Println()
		fmt.Println("  In-cluster models (no external key needed):")
		for _, m := range served {
			fmt.Printf("    local/%s\n", m)
		}
	}
	if len(advertised) > 0 {
		fmt.Println()
		fmt.Printf("  Advertised by the gateway: %s\n", strings.Join(advertised, ", "))
	}
	fmt.Println()
	if cfg.Model != "" {
		fmt.Printf("  Default model: %s (provider=%s, keyed=[%s])\n", cfg.Model, orDash(cfg.Provider), strings.Join(cfg.KeyedProviders, ", "))
	} else {
		fmt.Println("  No default model configured — run `adhar ai key set`.")
	}
	fmt.Println()
	return nil
}

// servedModels reads the model names in-cluster inference is actually serving,
// from the llm-d InferenceModel/InferencePool resources when present and from the
// vLLM Deployment's served-model argument otherwise. Either way the answer comes
// from the cluster rather than from a guess about what someone deployed.
func (p *platform) servedModels(ctx context.Context) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}

	for _, gvr := range []schema.GroupVersionResource{
		{Group: "inference.networking.x-k8s.io", Version: "v1alpha2", Resource: "inferencemodels"},
		{Group: "inference.networking.k8s.io", Version: "v1", Resource: "inferencemodels"},
	} {
		l, err := p.dyn.Resource(gvr).Namespace(p.ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			continue
		}
		for _, it := range l.Items {
			if name, ok, _ := nestedString(it.Object, "spec", "modelName"); ok {
				add(name)
			}
		}
	}

	// The vLLM Deployment carries the model id in --served-model-name or
	// --model; reading the args is how to know what a running server answers to.
	for _, name := range []string{"vllm", "llm-d-modelserver", "llm-d-vllm"} {
		d, err := p.clients.AppsV1().Deployments(p.ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			continue
		}
		for _, c := range d.Spec.Template.Spec.Containers {
			args := append([]string{}, c.Args...)
			args = append(args, c.Command...)
			for i, a := range args {
				switch {
				case strings.HasPrefix(a, "--served-model-name="):
					add(strings.TrimPrefix(a, "--served-model-name="))
				case strings.HasPrefix(a, "--model="):
					add(strings.TrimPrefix(a, "--model="))
				case (a == "--served-model-name" || a == "--model") && i+1 < len(args):
					add(args[i+1])
				}
			}
		}
	}
	return out
}

// providerForModel reports which key slot a model name needs, so a 401 can be
// explained before it happens.
func providerForModel(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.HasPrefix(m, "local/"), strings.HasPrefix(m, "vllm/"):
		return "none (in-cluster)"
	case strings.HasPrefix(m, "claude-"), strings.HasPrefix(m, "anthropic/"):
		return "anthropic"
	default:
		return "openai"
	}
}

// ensureContext is a tiny helper the chat/ask paths share: a cancelled parent
// context must abort a streaming answer promptly.
func ensureContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
