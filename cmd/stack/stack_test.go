/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The editor is the risky part: it rewrites files a human maintains, in a
// repository the user will commit by hand. What must be true is narrow and
// testable — the right line changes, the comments survive, and a name that is a
// prefix of another is never confused for it.

const sampleAppSet = `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: helm-charts-production
  namespace: adhar-system
spec:
  generators:
    - list:
        elements:
          # Harbor is the in-cluster registry. Heavy: a database, Redis, four
          # services. Off locally for that reason.
          - name: "harbor"
            enabled: "false"
            namespace: "adhar-system"
            category: "application"
            manifestPath: "application/harbor/manifests"
          # The AI data plane. Hard-depends on adhar-ai.
          - name: "agentgateway"
            enabled: "false"
            namespace: "adhar-system"
            category: "ai"
            manifestPath: "ai/agentgateway/manifests"
          - name: "adhar-ai"
            enabled: "false"
            namespace: "adhar-system"
            category: "ai"
            manifestPath: "ai/adhar-ai/manifests"
          # vault is superseded by openbao; exactly one of the two.
          - name: "vault"
            enabled: "false"
            namespace: "adhar-system"
            category: "security"
            manifestPath: "security/vault/manifests"
          - name: "openbao"
            enabled: "true"
            namespace: "adhar-system"
            category: "security"
            manifestPath: "security/openbao/manifests"
`

func writeSample(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "adhar-appset-production.yaml")
	if err := os.WriteFile(path, []byte(sampleAppSet), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSetEnabledChangesOneLineAndKeepsEveryComment(t *testing.T) {
	t.Parallel()
	path := writeSample(t)

	changed, err := setEnabled(path, "harbor", true)
	if err != nil || !changed {
		t.Fatalf("enabling harbor: changed=%v err=%v", changed, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(after)

	// The comments are the file's documentation — round-tripping through a YAML
	// marshaller would delete all of them and make a one-line change unreviewable.
	for _, comment := range []string{
		"# Harbor is the in-cluster registry.",
		"# services. Off locally for that reason.",
		"# The AI data plane. Hard-depends on adhar-ai.",
		"# vault is superseded by openbao; exactly one of the two.",
	} {
		if !strings.Contains(got, comment) {
			t.Errorf("comment lost: %q", comment)
		}
	}

	// Exactly one line differs, and it is harbor's.
	beforeLines := strings.Split(sampleAppSet, "\n")
	afterLines := strings.Split(got, "\n")
	if len(beforeLines) != len(afterLines) {
		t.Fatalf("line count changed: %d → %d", len(beforeLines), len(afterLines))
	}
	diffs := 0
	for i := range beforeLines {
		if beforeLines[i] != afterLines[i] {
			diffs++
			if !strings.Contains(afterLines[i], `enabled: "true"`) {
				t.Errorf("unexpected change on line %d: %q → %q", i+1, beforeLines[i], afterLines[i])
			}
		}
	}
	if diffs != 1 {
		t.Errorf("%d lines changed, want exactly 1", diffs)
	}

	// Indentation is preserved, because YAML is whitespace-significant and a
	// reindented line would break the parse it is meant to survive.
	for _, line := range afterLines {
		if strings.Contains(line, `enabled: "true"`) && !strings.HasPrefix(line, "            enabled:") {
			t.Errorf("indentation changed: %q", line)
		}
	}

	// And the value really parses back.
	elements, err := readElements(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range elements {
		if e.Name == "harbor" && !e.on() {
			t.Error("harbor still reads as disabled after being enabled")
		}
		if e.Name == "agentgateway" && e.on() {
			t.Error("agentgateway was enabled as a side effect")
		}
	}
}

func TestSetEnabledIsIdempotentAndHonestAboutUnknownPackages(t *testing.T) {
	t.Parallel()
	path := writeSample(t)

	// Already-in-that-state is a no-op, not an error: `enable x` twice must work.
	if changed, err := setEnabled(path, "openbao", true); err != nil || changed {
		t.Errorf("re-enabling an enabled package: changed=%v err=%v", changed, err)
	}
	before, _ := os.ReadFile(path)
	if string(before) != sampleAppSet {
		t.Error("a no-op edit rewrote the file")
	}

	// An unknown package must fail loudly rather than silently doing nothing —
	// otherwise a typo reads as success and the operator waits for a change that
	// will never come.
	if _, err := setEnabled(path, "harbour", true); err == nil {
		t.Error("a misspelled package name was accepted")
	}
}

func TestSetEnabledNeverTakesANeighboursLine(t *testing.T) {
	t.Parallel()
	// An element with no `enabled:` line of its own must not have the NEXT
	// element's line changed on its behalf.
	dir := t.TempDir()
	path := filepath.Join(dir, "appset.yaml")
	content := `spec:
  generators:
    - list:
        elements:
          - name: "no-gate"
            namespace: "adhar-system"
            category: "core"
            manifestPath: "core/no-gate/manifests"
          - name: "neighbour"
            enabled: "false"
            namespace: "adhar-system"
            category: "core"
            manifestPath: "core/neighbour/manifests"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := setEnabled(path, "no-gate", true); err == nil {
		t.Error("an element with no enabled: line should be reported, not silently skipped")
	}
	after, _ := os.ReadFile(path)
	if string(after) != content {
		t.Error("the neighbour's enabled: line was modified")
	}
}

func TestHardConflictsAndAdvisoriesAreDifferentThings(t *testing.T) {
	t.Parallel()
	elements := []element{
		{Name: "openbao", Enabled: "true"},
		{Name: "vault", Enabled: "false"},
		{Name: "rustfs", Enabled: "true"},
		{Name: "minio", Enabled: "false"},
		{Name: "supply-chain-policies", Enabled: "true"},
		{Name: "supply-chain-policies-enforce", Enabled: "true"},
	}

	// A real object collision refuses.
	if got := conflictsWith("vault", elements); len(got) != 1 || got[0] != "openbao" {
		t.Errorf("conflictsWith(vault) = %v, want [openbao]", got)
	}
	// A policy overlap only advises.
	if got := conflictsWith("minio", elements); len(got) != 0 {
		t.Errorf("conflictsWith(minio) = %v, want none — it is an advisory, not a collision", got)
	}
	if got := advisoriesFor("minio", elements); len(got) != 1 || got[0] != "rustfs" {
		t.Errorf("advisoriesFor(minio) = %v, want [rustfs]", got)
	}
	// The audit and enforce packs coexist by design: production ships both, so
	// treating them as exclusive would make `adhar stack conflicts` report a
	// violation on a correct platform.
	if got := conflictsWith("supply-chain-policies-enforce", elements); len(got) != 0 {
		t.Errorf("the enforce pack must not conflict with the audit pack: %v", got)
	}
}

func TestDependenciesAreReportedInBothDirections(t *testing.T) {
	t.Parallel()
	elements := []element{
		{Name: "agentgateway", Enabled: "true"},
		{Name: "adhar-ai", Enabled: "false"},
		{Name: "vllm-cpu", Enabled: "true"},
		{Name: "vllm", Enabled: "false"},
	}
	if got := missingDependencies("agentgateway", elements); len(got) != 1 || got[0] != "adhar-ai" {
		t.Errorf("missingDependencies(agentgateway) = %v, want [adhar-ai]", got)
	}
	if got := missingDependencies("vllm-cpu", elements); len(got) != 1 || got[0] != "vllm" {
		t.Errorf("missingDependencies(vllm-cpu) = %v, want [vllm]", got)
	}
	// Disabling adhar-ai must name agentgateway, which would keep running with
	// nothing behind it.
	if got := dependentsOf("adhar-ai", elements); len(got) != 1 || got[0] != "agentgateway" {
		t.Errorf("dependentsOf(adhar-ai) = %v, want [agentgateway]", got)
	}
	// Nothing enabled depends on vllm's profiles the other way round.
	if got := dependentsOf("vllm-gpu", elements); len(got) != 0 {
		t.Errorf("dependentsOf(vllm-gpu) = %v, want none", got)
	}
}

func TestProfileResolutionNamesBothFilesThatMustAgree(t *testing.T) {
	t.Parallel()
	p, err := profileFor("production", "platform/stack")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p.appsetFile, "adhar-appset-production.yaml") {
		t.Errorf("appset file = %q", p.appsetFile)
	}
	if !strings.HasSuffix(p.envFile, filepath.Join("environments", "production", "config.yaml")) {
		t.Errorf("environment file = %q", p.envFile)
	}
	if _, err := profileFor("staging", "platform/stack"); err == nil {
		t.Error("an unknown profile was accepted")
	}
}

// The real stack must satisfy the invariants this command enforces, so the command
// and the repository cannot drift apart.
func TestTheShippedProfilesSatisfyTheCommandsOwnRules(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"local", "production"} {
		p, err := profileFor(name, filepath.Join("..", "..", "platform", "stack"))
		if err != nil {
			t.Fatal(err)
		}
		elements, err := readElements(p.appsetFile)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, e := range elements {
			if !e.on() {
				continue
			}
			if c := conflictsWith(e.Name, elements); len(c) > 0 {
				t.Errorf("%s profile enables %s alongside %v, which collide", name, e.Name, c)
			}
			if m := missingDependencies(e.Name, elements); len(m) > 0 {
				t.Errorf("%s profile enables %s without %v", name, e.Name, m)
			}
		}
	}
}
