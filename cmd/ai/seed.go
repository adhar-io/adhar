/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package ai

import (
	"fmt"
	"os"
	"strings"
)

// Seeding the LLM credential at bring-up time.
//
// `adhar ai key set` writes straight into OpenBao, which only works once OpenBao is
// running — i.e. well after `adhar up` returns. That left a gap the operator had to
// close by hand on every fresh cluster, and forgetting it is indistinguishable from
// a broken AI stack: the packages come up, the ExternalSecret reports Degraded, and
// nothing says why.
//
// So `adhar up` stages the credential as a cluster-only Secret and the OpenBao
// bootstrap Job — which re-runs on every sync, is idempotent, and already holds the
// root token — imports it into the KV store the moment OpenBao is initialised.
// Nothing about this path touches Git: the value goes from the operator's
// environment into one Secret in one cluster.
//
// The KEY is read from the environment only. Not a flag (argv is world-readable in
// `ps` and lands in shell history), not a config field (that is Git), and not a
// file.

// SeedSecretName is the cluster-only Secret `adhar up` writes and the OpenBao
// bootstrap Job imports. It is namespaced to the platform namespace.
const SeedSecretName = "adhar-ai-llm-seed"

// LLMSeed is the non-secret shape of a staged credential, plus the key itself.
type LLMSeed struct {
	// Provider is the KV `PROVIDER` value the gateway keys off ("claude",
	// "openai", "openai-compatible", "local").
	Provider string
	Model    string
	Endpoint string
	// APIKey is empty for `local`, which needs no credential.
	APIKey string
	// Which slots the key belongs in. A provider fills one or the other; the
	// gateway reads a different Secret key per backend, so putting it in the wrong
	// slot leaves the backend unkeyed and 401ing.
	SetAnthropic bool
	SetOpenAI    bool
}

// Fields renders the seed as the Secret data the OpenBao bootstrap Job imports.
// The layout mirrors what hack/seed-adhar-ai-llm.sh and `adhar ai key set` write,
// so all three paths produce the same KV entry.
func (s LLMSeed) Fields() map[string]string {
	out := map[string]string{
		"PROVIDER": s.Provider,
		"MODEL":    s.Model,
		"ENDPOINT": s.Endpoint,
	}
	// Every property the ExternalSecret lists must EXIST, even empty: an absent
	// property fails the whole ExternalSecret rather than one key.
	out["API_KEY"] = ""
	out["ANTHROPIC_API_KEY"] = ""
	out["OPENAI_API_KEY"] = ""
	if s.SetAnthropic {
		out["API_KEY"] = s.APIKey
		out["ANTHROPIC_API_KEY"] = s.APIKey
	}
	if s.SetOpenAI {
		out["OPENAI_API_KEY"] = s.APIKey
	}
	out["BUDGET_PER_USER_DAILY_TOKENS"] = "2000000"
	out["BUDGET_PER_OP_MAX_TOOL_CALLS"] = "40"
	return out
}

// LLMSeedFromEnvironment builds a seed from the environment, or returns nil when the
// operator supplied nothing — which is a supported, silent outcome: the AI stack
// installs unkeyed and the platform is unaffected.
//
//	ADHAR_AI_LLM_API_KEY   the provider key (required unless the provider is `local`)
//	ADHAR_AI_LLM_PROVIDER  anthropic | openai | openrouter | openai-compatible | local
//	ADHAR_AI_LLM_MODEL     model id; a per-provider default is used when absent
//	ADHAR_AI_LLM_ENDPOINT  base URL, for openai-compatible / self-hosted
func LLMSeedFromEnvironment() (*LLMSeed, error) {
	key := strings.TrimSpace(os.Getenv("ADHAR_AI_LLM_API_KEY"))
	provider := strings.TrimSpace(os.Getenv("ADHAR_AI_LLM_PROVIDER"))
	model := strings.TrimSpace(os.Getenv("ADHAR_AI_LLM_MODEL"))
	endpoint := strings.TrimSpace(os.Getenv("ADHAR_AI_LLM_ENDPOINT"))

	// "Nothing supplied" means exactly that: no key AND no provider. If the
	// operator named a provider, they meant to key the platform, and a missing key
	// is a mistake worth reporting — silently installing unkeyed is how someone
	// ends up staring at a Degraded ExternalSecret wondering what they did wrong.
	if key == "" && provider == "" {
		return nil, nil
	}
	if provider == "" {
		// A key with no provider named: infer from the key's own prefix rather than
		// defaulting to anthropic and 401ing against the wrong backend. OpenRouter
		// and Anthropic keys are unambiguous; anything else is OpenAI-shaped.
		switch {
		case strings.HasPrefix(key, "sk-or-"):
			provider = "openrouter"
		case strings.HasPrefix(key, "sk-ant-"):
			provider = "anthropic"
		default:
			provider = "openai"
		}
	}

	prof, err := profileFor(provider, model, endpoint)
	if err != nil {
		return nil, err
	}
	if prof.needsKey && key == "" {
		return nil, fmt.Errorf("ADHAR_AI_LLM_PROVIDER=%s needs ADHAR_AI_LLM_API_KEY", provider)
	}
	return &LLMSeed{
		Provider:     prof.kvProvider,
		Model:        prof.model,
		Endpoint:     prof.endpoint,
		APIKey:       key,
		SetAnthropic: prof.setAnthropic,
		SetOpenAI:    prof.setOpenAI,
	}, nil
}

// Describe reports what was staged, without the key. Used in bring-up output, so it
// must never be able to print a credential.
func (s LLMSeed) Describe() string {
	slots := []string{}
	if s.SetAnthropic {
		slots = append(slots, "anthropic")
	}
	if s.SetOpenAI {
		slots = append(slots, "openai")
	}
	if len(slots) == 0 {
		slots = append(slots, "none (in-cluster inference)")
	}
	return fmt.Sprintf("provider=%s model=%s slots=[%s]", s.Provider, s.Model, strings.Join(slots, ", "))
}
