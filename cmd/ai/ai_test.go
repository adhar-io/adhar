/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package ai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// The guarantees worth testing here are the safety ones. Everything else in this
// package is a call to the cluster or to a model, but the rules that keep an
// agentic run honest — authority only narrows, write targets are checked, secrets
// are never returned — are pure functions, and they are the ones a future change
// could quietly break.

func TestAutonomyOnlyEverNarrows(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ceiling, requested, want string
		wantErr                  bool
	}{
		{autonomySuggest, "", autonomySuggest, false},
		{autonomySuggest, autonomyReadOnly, autonomyReadOnly, false},
		{autonomySuggest, autonomySuggest, autonomySuggest, false},
		{autonomyApprove, autonomySuggest, autonomySuggest, false},
		{autonomyScoped, autonomyApprove, autonomyApprove, false},
		// Above the ceiling must fail, not clamp: a caller who asked for
		// unattended writes and silently got `suggest` would believe the run was
		// unattended.
		{autonomySuggest, autonomyApprove, "", true},
		{autonomyReadOnly, autonomySuggest, "", true},
		// A typo must not fall back to a default that grants more than read-only.
		{autonomySuggest, "readonly", "", true},
		{autonomySuggest, "yolo", "", true},
	}
	for _, c := range cases {
		got, err := effectiveAutonomy(c.ceiling, c.requested)
		if c.wantErr {
			if err == nil {
				t.Errorf("effectiveAutonomy(%q, %q) = %q, want an error", c.ceiling, c.requested, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("effectiveAutonomy(%q, %q) unexpected error: %v", c.ceiling, c.requested, err)
			continue
		}
		if got != c.want {
			t.Errorf("effectiveAutonomy(%q, %q) = %q, want %q", c.ceiling, c.requested, got, c.want)
		}
	}
}

func TestReadOnlyRunIsNotEvenOfferedAWriteTool(t *testing.T) {
	t.Parallel()
	ac := defaultAgentConfig()
	for _, tl := range builtinTools(autonomyReadOnly, ac) {
		if tl.write {
			t.Fatalf("read-only run was offered write tool %q", tl.name)
		}
	}
	// And at suggest it is offered, or the rung would mean nothing.
	var found bool
	for _, tl := range builtinTools(autonomySuggest, ac) {
		if tl.write {
			found = true
		}
	}
	if !found {
		t.Fatal("suggest run was offered no write tool")
	}
}

func TestScopedAutonomyRefusesWritesUntilItsAllowListIsEnumerated(t *testing.T) {
	t.Parallel()
	ac := defaultAgentConfig() // ships with an empty writePolicy.scoped
	ok, _, _, why := ac.writesAllowed(autonomyScoped)
	if ok {
		t.Fatal("scoped autonomy allowed a write with an empty scoped allow-list; that would be a silent widening")
	}
	if !strings.Contains(why, "scoped") {
		t.Fatalf("refusal does not say which field to set: %q", why)
	}

	ac.ScopedRepos = []string{"environments"}
	ac.ScopedPaths = []string{"environments/dev/"}
	ok, repos, paths, _ := ac.writesAllowed(autonomyScoped)
	if !ok || len(repos) != 1 || len(paths) != 1 {
		t.Fatalf("scoped autonomy with an allow-list: ok=%v repos=%v paths=%v", ok, repos, paths)
	}
}

func TestWriteTargetsAreCheckedAgainstThePolicy(t *testing.T) {
	t.Parallel()
	repos := []string{"packages", "environments"}
	paths := []string{"packages/", "environments/"}
	good := map[string]interface{}{"repo": "packages", "path": "packages/data/metabase/manifests/install.yaml"}
	if err := checkWriteTarget(good, repos, paths); err != nil {
		t.Fatalf("allowed target rejected: %v", err)
	}
	for name, args := range map[string]map[string]interface{}{
		"repo outside the policy": {"repo": "adhar", "path": "packages/x.yaml"},
		"path outside the policy": {"repo": "packages", "path": "../../etc/passwd"},
		"path in another tree":    {"repo": "packages", "path": "hack/seed.sh"},
		"missing path":            {"repo": "packages"},
		"missing repo":            {"path": "packages/x.yaml"},
	} {
		if err := checkWriteTarget(args, repos, paths); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestSecretValuesAreNeverReturnedToAModel(t *testing.T) {
	t.Parallel()
	obj := map[string]interface{}{
		"kind":     "Secret",
		"metadata": map[string]interface{}{"name": "adhar-ai-llm"},
		"data": map[string]interface{}{
			"openaiApiKey": "c2stcHJvai1ERUFEQkVFRg==",
			"provider":     "b3BlbmFp",
		},
		"stringData": map[string]interface{}{"apiKey": "sk-literal-secret"},
	}
	out := redact(obj)
	blob, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"c2stcHJvai1ERUFEQkVFRg==", "sk-literal-secret", "b3BlbmFp"} {
		if strings.Contains(string(blob), leaked) {
			t.Fatalf("redact leaked %q: %s", leaked, blob)
		}
	}
	// The key NAMES must survive: "the slot exists but is empty" is the whole
	// diagnostic value of reading the Secret.
	if !strings.Contains(string(blob), "openaiApiKey") {
		t.Fatalf("redact dropped the key names: %s", blob)
	}

	// A non-Secret must pass through untouched, or every other read would lose
	// the data the agent needs.
	cm := map[string]interface{}{"kind": "ConfigMap", "data": map[string]interface{}{"config.yaml": "autonomy: {default: suggest}"}}
	if got := redact(cm); got["data"].(map[string]interface{})["config.yaml"] != "autonomy: {default: suggest}" {
		t.Fatalf("redact altered a ConfigMap: %v", got)
	}
}

func TestProviderProfilesMatchTheGatewayRoutingTable(t *testing.T) {
	t.Parallel()
	// OpenRouter is OpenAI-compatible, so its key must land in the OpenAI slot
	// and its default model must begin with a prefix the OpenAI route matches —
	// otherwise the request falls through to Anthropic's backend and 401s.
	p, err := profileFor("openrouter", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !p.setOpenAI || p.setAnthropic {
		t.Fatalf("openrouter must fill the OpenAI slot only: %+v", p)
	}
	if providerForModel(p.model) != "openai" {
		t.Fatalf("openrouter default model %q does not route to the OpenAI backend", p.model)
	}
	if p.endpoint == "" {
		t.Fatal("openrouter needs a default endpoint")
	}

	a, err := profileFor("anthropic", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !a.setAnthropic || a.setOpenAI {
		t.Fatalf("anthropic must fill the anthropic slot only: %+v", a)
	}
	if providerForModel(a.model) != "anthropic" {
		t.Fatalf("anthropic default model %q does not route to the anthropic backend", a.model)
	}

	l, err := profileFor("local", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if l.needsKey {
		t.Fatal("in-cluster inference must not require a key")
	}
	if providerForModel(l.model) != "none (in-cluster)" {
		t.Fatalf("local default model %q does not route in-cluster", l.model)
	}

	if _, err := profileFor("openai-compatible", "", ""); err == nil {
		t.Fatal("openai-compatible without an endpoint or model must be refused")
	}
	if _, err := profileFor("gemini", "", ""); err == nil {
		t.Fatal("an unknown provider must be refused rather than defaulted")
	}
}

func TestAgentConfigIsReadFromTheClusterAndNeverWidensOnFailure(t *testing.T) {
	t.Parallel()
	const cfg = `
autonomy:
  default: read-only
limits:
  maxSteps: 5
  maxToolCallsPerOp: 9
writePolicy:
  allowedRepos: [environments]
  allowedPathPrefixes: ["environments/"]
  scoped:
    allowedRepos: [environments]
    allowedPathPrefixes: ["environments/dev/"]
`
	p := &platform{ns: "adhar-system", clients: fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: agentConfigMap, Namespace: "adhar-system"},
		Data:       map[string]string{"config.yaml": cfg},
	})}
	ac := p.readAgentConfig(context.Background())
	if ac.Level != autonomyReadOnly || ac.MaxSteps != 5 || ac.MaxToolCalls != 9 {
		t.Fatalf("cluster config not honoured: %+v", ac)
	}
	if len(ac.ScopedPaths) != 1 || ac.ScopedPaths[0] != "environments/dev/" {
		t.Fatalf("scoped allow-list not read: %+v", ac)
	}

	// An unparseable config must not raise authority above the shipped default.
	broken := &platform{ns: "adhar-system", clients: fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: agentConfigMap, Namespace: "adhar-system"},
		Data:       map[string]string{"config.yaml": "autonomy: [this is not a map"},
	})}
	if got := broken.readAgentConfig(context.Background()); rank(got.Level) > rank(autonomySuggest) {
		t.Fatalf("a broken config widened authority to %q", got.Level)
	}

	// No ConfigMap at all (the AI package is disabled by default) behaves like
	// the shipped default rather than more freely.
	absent := &platform{ns: "adhar-system", clients: fake.NewSimpleClientset()}
	if got := absent.readAgentConfig(context.Background()); got.Level != autonomySuggest {
		t.Fatalf("missing config gave level %q", got.Level)
	}
}

func TestLLMConfigReportsKeyedSlotsWithoutHoldingAKey(t *testing.T) {
	t.Parallel()
	p := &platform{ns: "adhar-system", clients: fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: llmSecret, Namespace: "adhar-system"},
		Data: map[string][]byte{
			"provider":     []byte("openai"),
			"model":        []byte("openai/gpt-4o-mini"),
			"openaiApiKey": []byte("sk-or-v1-secret"),
		},
	})}
	cfg, err := p.readLLMConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Keyed || len(cfg.KeyedProviders) != 1 || cfg.KeyedProviders[0] != "openai" {
		t.Fatalf("keyed slots wrong: %+v", cfg)
	}
	blob, _ := json.Marshal(cfg)
	if strings.Contains(string(blob), "sk-or-v1-secret") {
		t.Fatalf("llmConfig carried the key: %s", blob)
	}

	// An absent Secret is the normal state before seeding, not an error.
	empty := &platform{ns: "adhar-system", clients: fake.NewSimpleClientset()}
	cfg, err = empty.readLLMConfig(context.Background())
	if err != nil || cfg.SecretPresent || cfg.Keyed {
		t.Fatalf("absent secret: cfg=%+v err=%v", cfg, err)
	}
}

func TestPodProblemNamesTheRealReason(t *testing.T) {
	t.Parallel()
	healthy := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "ok", Namespace: "adhar-system"},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
	if got := podProblem(healthy); got != "" {
		t.Fatalf("healthy pod reported as a problem: %q", got)
	}

	// The reason a pod is broken lives in container state, not the phase: a
	// CrashLoopBackOff pod is Phase=Running.
	crashing := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "metabase-0", Namespace: "adhar-system"},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:         "metabase",
				RestartCount: 90,
				State:        corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				LastTerminationState: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{ExitCode: 137, Reason: "OOMKilled"},
				},
			}},
		},
	}
	got := podProblem(crashing)
	for _, want := range []string{"CrashLoopBackOff", "restarts=90", "OOMKilled", "137"} {
		if !strings.Contains(got, want) {
			t.Errorf("podProblem() = %q, missing %q", got, want)
		}
	}
}

func TestFederatedToolsAreClassifiedConservatively(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"gitops_propose_change", "provision_create_database", "catalog_open_pr", "cluster_restart_deployment"} {
		if !looksLikeWrite(name) {
			t.Errorf("%q should be treated as a write and prompt for confirmation", name)
		}
	}
	for _, name := range []string{"cluster_get_pod", "observability_promql", "security_findings", "cost_showback"} {
		if looksLikeWrite(name) {
			t.Errorf("%q is a read and should not prompt", name)
		}
	}
}

func TestMCPResponsesAreReadInBothFramings(t *testing.T) {
	t.Parallel()
	plain := []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`)
	if got := string(extractSSEData(plain)); got != string(plain) {
		t.Fatalf("plain JSON was altered: %s", got)
	}
	sse := []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"tools\":[]}}\n\n")
	var out rpcResponse
	if err := json.Unmarshal(extractSSEData(sse), &out); err != nil {
		t.Fatalf("SSE framing not unwrapped: %v", err)
	}
}

func TestToolArgumentsParseTypedValues(t *testing.T) {
	t.Parallel()
	got, err := parseArgs([]string{"namespace=adhar-system", "limit=5", "previous=true", `filter={"a":1}`})
	if err != nil {
		t.Fatal(err)
	}
	if got["namespace"] != "adhar-system" {
		t.Errorf("string argument mangled: %v", got["namespace"])
	}
	if got["limit"] != float64(5) {
		t.Errorf("numeric argument not typed: %#v", got["limit"])
	}
	if got["previous"] != true {
		t.Errorf("boolean argument not typed: %#v", got["previous"])
	}
	if _, ok := got["filter"].(map[string]interface{}); !ok {
		t.Errorf("object argument not typed: %#v", got["filter"])
	}
	if _, err := parseArgs([]string{"novalue"}); err == nil {
		t.Error("an argument without = must be refused")
	}
}

func TestTraceNeverPrintsAProposalBody(t *testing.T) {
	t.Parallel()
	// The live trace is the audit trail a human reads; a 4 KB file body in it
	// would bury every other line.
	body := strings.Repeat("x", 4000)
	line := compactArgs(map[string]interface{}{"repo": "packages", "path": "packages/a.yaml", "content": body})
	if strings.Contains(line, body) {
		t.Fatal("compactArgs printed the whole proposal body")
	}
	if !strings.Contains(line, "path=packages/a.yaml") {
		t.Fatalf("compactArgs dropped the useful arguments: %q", line)
	}
}
