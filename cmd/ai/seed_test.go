/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package ai

import (
	"strings"
	"testing"
)

// The seed path runs unattended inside `adhar up`, so its mistakes are silent ones:
// a key in the wrong slot leaves the gateway 401ing, and a missing property fails
// the whole ExternalSecret rather than one field.

func TestSeedFieldsAlwaysDeclareEveryPropertyTheExternalSecretReads(t *testing.T) {
	t.Parallel()
	seed := LLMSeed{Provider: "openai", Model: "openai/gpt-4o-mini", APIKey: "sk-or-v1-x", SetOpenAI: true}
	fields := seed.Fields()

	// The ExternalSecret lists each of these; an ABSENT property fails the whole
	// secret, while an empty string is fine.
	for _, required := range []string{
		"PROVIDER", "MODEL", "ENDPOINT",
		"API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY",
		"BUDGET_PER_USER_DAILY_TOKENS", "BUDGET_PER_OP_MAX_TOOL_CALLS",
	} {
		if _, ok := fields[required]; !ok {
			t.Errorf("seed omits %q, which fails the adhar-ai-llm ExternalSecret entirely", required)
		}
	}
	if fields["OPENAI_API_KEY"] != "sk-or-v1-x" {
		t.Errorf("the key did not land in the OpenAI slot: %q", fields["OPENAI_API_KEY"])
	}
	if fields["ANTHROPIC_API_KEY"] != "" {
		t.Errorf("the key leaked into the anthropic slot: %q", fields["ANTHROPIC_API_KEY"])
	}
}

func TestSeedFromEnvironmentInfersTheProviderFromTheKey(t *testing.T) {
	cases := []struct {
		key          string
		wantProvider string
		wantOpenAI   bool
		wantAnthro   bool
	}{
		// An OpenRouter key defaulted to anthropic would 401 against the wrong
		// backend with a message about credentials, not about routing.
		{"sk-or-v1-abc", "openai", true, false},
		{"sk-ant-abc", "claude", false, true},
		{"sk-proj-abc", "openai", true, false},
	}
	for _, c := range cases {
		t.Setenv("ADHAR_AI_LLM_API_KEY", c.key)
		t.Setenv("ADHAR_AI_LLM_PROVIDER", "")
		t.Setenv("ADHAR_AI_LLM_MODEL", "")
		t.Setenv("ADHAR_AI_LLM_ENDPOINT", "")

		seed, err := LLMSeedFromEnvironment()
		if err != nil || seed == nil {
			t.Fatalf("key %q: seed=%v err=%v", c.key, seed, err)
		}
		if seed.Provider != c.wantProvider || seed.SetOpenAI != c.wantOpenAI || seed.SetAnthropic != c.wantAnthro {
			t.Errorf("key %q gave provider=%s openai=%v anthropic=%v", c.key, seed.Provider, seed.SetOpenAI, seed.SetAnthropic)
		}
		// An OpenRouter key must also get an endpoint and a model the OpenAI route
		// actually matches, or the request falls through to the wrong backend.
		if strings.HasPrefix(c.key, "sk-or-") {
			if seed.Endpoint == "" {
				t.Error("an OpenRouter key was staged with no endpoint")
			}
			if providerForModel(seed.Model) != "openai" {
				t.Errorf("OpenRouter default model %q does not route to the OpenAI backend", seed.Model)
			}
		}
	}
}

func TestSeedIsAbsentWhenNothingWasSupplied(t *testing.T) {
	t.Setenv("ADHAR_AI_LLM_API_KEY", "")
	t.Setenv("ADHAR_AI_LLM_PROVIDER", "")
	seed, err := LLMSeedFromEnvironment()
	if err != nil || seed != nil {
		t.Fatalf("no environment should stage nothing and not error: seed=%v err=%v", seed, err)
	}

	// `local` needs no key at all, and must still stage so the platform records
	// which model to route to.
	t.Setenv("ADHAR_AI_LLM_PROVIDER", "local")
	seed, err = LLMSeedFromEnvironment()
	if err != nil || seed == nil {
		t.Fatalf("local provider: seed=%v err=%v", seed, err)
	}
	if seed.SetOpenAI || seed.SetAnthropic {
		t.Error("in-cluster inference must not fill a hosted key slot")
	}

	// A provider that needs a key, with none, is an error worth reporting.
	t.Setenv("ADHAR_AI_LLM_PROVIDER", "anthropic")
	t.Setenv("ADHAR_AI_LLM_API_KEY", "")
	if _, err := LLMSeedFromEnvironment(); err == nil {
		t.Error("anthropic with no key should be reported, not staged empty")
	}
}

func TestDescribeCannotPrintTheKey(t *testing.T) {
	t.Parallel()
	seed := LLMSeed{Provider: "openai", Model: "gpt-4o", APIKey: "sk-or-v1-SUPERSECRET", SetOpenAI: true}
	out := seed.Describe()
	if strings.Contains(out, "SUPERSECRET") {
		t.Fatalf("Describe leaked the key: %s", out)
	}
	if !strings.Contains(out, "openai") {
		t.Errorf("Describe should name the slot it filled: %s", out)
	}
}
