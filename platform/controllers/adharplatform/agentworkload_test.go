package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// The AgentWorkload XRD and its composition must agree on the parameters.
//
// A parameter the XRD declares but the composition never reads is a knob that
// does nothing — the operator sets `tools.allow` and the agent is still denied,
// with no error anywhere. A parameter the composition reads but the XRD does
// not declare is silently dropped by the apiserver's schema pruning, so the
// composition sees its default forever. Both failures are invisible at apply
// time, which is why they are checked here rather than discovered on a cluster.
func TestAgentWorkloadXRDAndCompositionAgree(t *testing.T) {
	xrdPath := filepath.Join(controlPlaneDir(t), "xrd/agentworkload.xrd.yaml")
	compPath := filepath.Join(controlPlaneDir(t), "compositions/agent/kubernetes.yaml")

	var xrd struct {
		Spec struct {
			Names                 struct{ Kind string }
			DefaultCompositionRef struct{ Name string } `json:"defaultCompositionRef"`
			Versions              []struct {
				Schema struct {
					OpenAPIV3Schema map[string]interface{} `json:"openAPIV3Schema"`
				}
			}
		}
	}
	b, err := os.ReadFile(xrdPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(b, &xrd); err != nil {
		t.Fatalf("XRD does not parse: %v", err)
	}
	params := xrd.Spec.Versions[0].Schema.OpenAPIV3Schema["properties"].(map[string]interface{})["spec"].(map[string]interface{})["properties"].(map[string]interface{})["parameters"].(map[string]interface{})["properties"].(map[string]interface{})

	cb, err := os.ReadFile(compPath)
	if err != nil {
		t.Fatal(err)
	}
	comp := string(cb)
	var c struct {
		Metadata struct{ Name string }
		Spec     struct {
			CompositeTypeRef struct{ Kind string } `json:"compositeTypeRef"`
		}
	}
	if err := yaml.Unmarshal(cb, &c); err != nil {
		t.Fatalf("composition does not parse: %v", err)
	}
	if c.Spec.CompositeTypeRef.Kind != xrd.Spec.Names.Kind {
		t.Errorf("composition targets %q, XRD defines %q", c.Spec.CompositeTypeRef.Kind, xrd.Spec.Names.Kind)
	}
	if c.Metadata.Name != xrd.Spec.DefaultCompositionRef.Name {
		t.Errorf("XRD defaults to composition %q but the composition is named %q — an AgentWorkload "+
			"with no compositionRef would never be reconciled", xrd.Spec.DefaultCompositionRef.Name, c.Metadata.Name)
	}

	// Every top-level parameter the XRD declares must be read by the template.
	for name := range params {
		if !strings.Contains(comp, `"`+name+`"`) {
			t.Errorf("XRD parameter %q is never read by the composition — setting it does nothing", name)
		}
	}
	// Every `dig "..."` first key in the template must be a declared parameter.
	digs := regexp.MustCompile(`dig "([a-zA-Z]+)"`).FindAllStringSubmatch(comp, -1)
	seen := map[string]bool{}
	for _, m := range digs {
		key := m[1]
		if key == "spec" || seen[key] {
			continue
		}
		seen[key] = true
		if _, ok := params[key]; !ok {
			t.Errorf("composition reads parameter %q which the XRD does not declare — the apiserver "+
				"prunes it, so the composition only ever sees the default", key)
		}
	}
}

// The agent's grant must be default-deny: an AgentWorkload with no models and
// no tools must produce a rule that admits NO LLM call and NO tool call.
//
// The composition encodes the empty model list as the literal `false` for
// /v1/ paths rather than as a regex. A regex that "matches nothing" is easy to
// get wrong — `^()$` matches the empty string, and a request with no model
// header has exactly that — and the result would be an agent with no declared
// models spending money through the gateway's fallback route.
func TestAgentWorkloadEmptyAllowListsDenyEverything(t *testing.T) {
	cb, err := os.ReadFile(filepath.Join(controlPlaneDir(t), "compositions/agent/kubernetes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	comp := string(cb)
	if !strings.Contains(comp, `$llmClause := "!request.path.startsWith(\"/v1/\")"`) {
		t.Error("with no models allowed, the LLM clause must be `!request.path.startsWith(\"/v1/\")` — " +
			"i.e. false for every LLM path — not a regex")
	}
	if !strings.Contains(comp, `$toolRule := printf "default(jwt.sub, \"\") == \"%s\" && false" $sub`) {
		t.Error("with no tools allowed, the backend tool rule must be the literal `false` for this subject — " +
			"no tool listed, no tool callable")
	}
	// The identity clause must be an exact match on the subject, never a prefix.
	if !strings.Contains(comp, `default(jwt.sub, "") == "{{ $sub }}" &&`) {
		t.Error("the identity clause must compare jwt.sub for equality with this agent's exact subject")
	}
}

// The identity the composition grants must be the identity the gateway and the
// metrics attribution recognise, and the namespace convention the budget tiers
// read the environment from.
func TestAgentWorkloadIdentityConventionIsShared(t *testing.T) {
	cb, err := os.ReadFile(filepath.Join(controlPlaneDir(t), "compositions/agent/kubernetes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	comp := string(cb)
	// <name>-<env> namespace, `agent` ServiceAccount.
	if !strings.Contains(comp, `$ns := printf "%s-%s" $name $env`) || !strings.Contains(comp, `$sa := "agent"`) {
		t.Fatal("the composition no longer names the namespace <name>-<env> with ServiceAccount `agent`")
	}
	// guardrails.yaml's agent tiers and security.yaml's environment label both
	// derive the environment from the subject's `-<env>:agent` suffix.
	for _, f := range []string{"security.yaml", "guardrails.yaml"} {
		b, err := os.ReadFile(filepath.Join(stackPackagesDir(t), "ai/agentgateway/manifests", f))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `endsWith("-prod:agent")`) {
			t.Errorf("%s does not read the environment from the `-<env>:agent` subject suffix the "+
				"composition produces — the two would silently disagree about which tier an agent is in", f)
		}
	}
	// The Kyverno pack selects the label the composition sets.
	kb, err := os.ReadFile(filepath.Join(stackPackagesDir(t), "ai/adhar-ai/manifests/agent-workload-guardrails.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(comp, `adhar.io/agent-isolation: "true"`) || !strings.Contains(string(kb), `adhar.io/agent-isolation: "true"`) {
		t.Error("the composition and the Kyverno pack must agree on the adhar.io/agent-isolation selector")
	}
}

// Both identities that create composed Objects must be allowed to create the
// kinds this composition emits. Crossplane's aggregate role gates the
// composition; provider-kubernetes's role is what actually applies the
// manifests. A kind missing from either fails at apply with a 403 the XR
// reports only as a generic sync error.
func TestAgentWorkloadComposedKindsAreGranted(t *testing.T) {
	for _, f := range []string{"rbac/local-compose-rbac.yaml", "providers/provider-runtime-rbac.yaml"} {
		b, err := os.ReadFile(filepath.Join(controlPlaneDir(t), f))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		for _, want := range []string{"agentgatewaypolicies", "prometheusrules", "limitranges", "networkpolicies", "serviceaccounts", "resourcequotas", "namespaces"} {
			if !strings.Contains(s, `"`+want+`"`) {
				t.Errorf("%s does not grant %s, which the agent composition emits", f, want)
			}
		}
	}
}

func controlPlaneDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("../../controlplane/configuration")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("control plane configuration not found at %s: %v", dir, err)
	}
	return dir
}

// Agent tokens must be validated by the SAME jwtAuthentication policy as human
// tokens, and the controller must be able to fetch the apiserver's keyset.
//
// Both halves were learned on a live cluster (2026-10-04). agentgateway applies
// one jwtAuthentication policy per target: a second policy object carrying the
// apiserver provider reported Accepted + Attached, the proxy's config dump
// listed its key id, and every agent token was still rejected with `token uses
// the unknown key` because only the first object's providers are consulted. And
// the controller — not the proxy — fetches keysets, with Go's system pool, so
// `https://kubernetes.default.svc.cluster.local` failed with `certificate
// signed by unknown authority` until the cluster CA was mounted into it.
func TestAgentIdentityRidesTheOneJWTPolicy(t *testing.T) {
	dir := filepath.Join(stackPackagesDir(t), "ai/agentgateway")
	sb, err := os.ReadFile(filepath.Join(dir, "manifests/security.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	type provider struct {
		Issuer    string   `json:"issuer"`
		Audiences []string `json:"audiences"`
	}
	var jwtPolicies []string
	var providers []provider
	for _, doc := range strings.Split(string(sb), "\n---\n") {
		var p struct {
			Kind     string
			Metadata struct{ Name string }
			Spec     struct {
				Traffic struct {
					JWT *struct {
						Providers []provider `json:"providers"`
					} `json:"jwtAuthentication"`
				} `json:"traffic"`
			}
		}
		if err := yaml.Unmarshal([]byte(doc), &p); err != nil {
			t.Fatalf("security.yaml does not parse: %v", err)
		}
		if p.Kind == "AgentgatewayPolicy" && p.Spec.Traffic.JWT != nil {
			jwtPolicies = append(jwtPolicies, p.Metadata.Name)
			providers = append(providers, p.Spec.Traffic.JWT.Providers...)
		}
	}
	if len(jwtPolicies) != 1 {
		t.Fatalf("security.yaml carries %d jwtAuthentication policies (%v); agentgateway consults only "+
			"one per target, so every provider must live in the same object", len(jwtPolicies), jwtPolicies)
	}
	agent := false
	for _, p := range providers {
		if p.Issuer == "https://kubernetes.default.svc.cluster.local" {
			agent = true
			if len(p.Audiences) != 1 || p.Audiences[0] != "agentgateway" {
				t.Errorf("the apiserver provider accepts audiences %v; it must accept only `agentgateway`, "+
					"or every pod's default token becomes a gateway credential", p.Audiences)
			}
		}
	}
	if !agent {
		t.Fatalf("policy %s has no provider for the apiserver issuer — AgentWorkload identities cannot "+
			"authenticate", jwtPolicies[0])
	}

	// The controller has to trust the cluster CA to fetch that provider's keys.
	ib, err := os.ReadFile(filepath.Join(dir, "manifests/install.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	install := string(ib)
	caDir := regexp.MustCompile(`name: SSL_CERT_DIR\s*\n\s*value: "([^"]+)"`).FindStringSubmatch(install)
	if caDir == nil {
		t.Fatal("install.yaml does not set SSL_CERT_DIR on the controller — the apiserver JWKS fetch " +
			"fails TLS verification (regenerate from values.yaml)")
	}
	mount := regexp.MustCompile(`mountPath: (\S+)\s*\n\s*name: cluster-ca`).FindStringSubmatch(install)
	if mount == nil || !strings.Contains(install, "name: kube-root-ca.crt") {
		t.Fatal("install.yaml does not mount the kube-root-ca.crt ConfigMap into the controller as `cluster-ca`")
	}
	if !strings.Contains(":"+caDir[1]+":", ":"+mount[1]+":") {
		t.Errorf("SSL_CERT_DIR %q does not include the cluster CA mount %q", caDir[1], mount[1])
	}
	if !strings.HasPrefix(caDir[1], "/etc/ssl/certs:") {
		t.Errorf("SSL_CERT_DIR %q replaces Go's default directories — the distro CA dir must stay listed first "+
			"or every PUBLIC JWKS/LLM endpoint the controller reaches loses its trust", caDir[1])
	}
}

// An Allow rule's matchExpressions are OR'd by the proxy — every rule must be
// ONE expression.
//
// The CRD describes the list as "CEL expressions that must all evaluate to
// true", but agentgateway v1.5.0 evaluates an Allow rule with `.any()` and its
// authorization docs say so too. Written as a list, the read tier's second
// clause (`!(mcp.tool.target in [PR targets])`) admitted EVERY authenticated
// token to every LLM call on its own, and the agent grant's tool clause did the
// same — a ServiceAccount token that matched no rule got 200s on 2026-10-04.
// Clauses are therefore joined with `&&` inside one string, here and in the
// composition that mints the per-agent grant.
func TestEveryAllowRuleIsOneExpression(t *testing.T) {
	sb, err := os.ReadFile(filepath.Join(stackPackagesDir(t), "ai/agentgateway/manifests/security.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	rules := 0
	var expressions []string
	for _, doc := range strings.Split(string(sb), "\n---\n") {
		var p struct {
			Kind     string
			Metadata struct{ Name string }
			Spec     struct {
				Traffic struct {
					Authorization *authzRule `json:"authorization"`
				} `json:"traffic"`
				Backend struct {
					MCP struct {
						Authorization *authzRule `json:"authorization"`
					} `json:"mcp"`
				} `json:"backend"`
			}
		}
		if err := yaml.Unmarshal([]byte(doc), &p); err != nil {
			t.Fatalf("security.yaml does not parse: %v", err)
		}
		// Tool rules live on the MCP backend (per-tool evaluation); the HTTP
		// rules on the Gateway. Both are Allow rule sets with the same OR trap.
		a := p.Spec.Traffic.Authorization
		if a == nil {
			a = p.Spec.Backend.MCP.Authorization
		}
		if p.Kind != "AgentgatewayPolicy" || a == nil {
			continue
		}
		rules++
		expressions = append(expressions, a.Policy.MatchExpressions...)
		if a.Action != "Allow" {
			t.Errorf("%s uses action %q; the rule set is Allow-only so an erroring expression can "+
				"lock people out but never let anyone in", p.Metadata.Name, a.Action)
		}
		if n := len(a.Policy.MatchExpressions); n != 1 {
			t.Errorf("%s has %d matchExpressions; the proxy OR's them, so clauses must be joined with "+
				"&& inside a single expression", p.Metadata.Name, n)
		}
	}
	if rules == 0 {
		t.Fatal("no authorization rules found in security.yaml — nothing was checked")
	}

	// The composition is a go-template, so it is checked textually: exactly one
	// list item under the grant's matchExpressions, carrying all three clauses.
	cb, err := os.ReadFile(filepath.Join(controlPlaneDir(t), "compositions/agent/kubernetes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	blocks := regexp.MustCompile(`matchExpressions:\n((?:\s+- .*\n)+)`).FindAllStringSubmatch(string(cb), -1)
	if len(blocks) != 2 {
		t.Fatalf("the agent composition emits %d authorization matchExpressions lists; expected two — the "+
			"HTTP grant (identity + models) on the Gateway and the tool grant on the MCP backend", len(blocks))
	}
	var all []string
	for _, b := range blocks {
		items := strings.Split(strings.TrimRight(b[1], "\n"), "\n")
		if len(items) != 1 {
			t.Fatalf("an agent grant has %d matchExpressions entries; its clauses must be one &&-joined "+
				"expression or each clause alone admits the request", len(items))
		}
		all = append(all, items[0])
	}
	joined := strings.Join(all, "\n")
	for _, clause := range []string{`default(jwt.sub, "") == "{{ $sub }}" && {{ $llmClause }}`, `{{ $toolRule }}`} {
		if !strings.Contains(joined, clause) {
			t.Errorf("the agent grants lost the clause %s", clause)
		}
	}
	// The tool grant must bind to the subject too — a tool rule without an
	// identity clause would grant every caller the union of all agents' tools.
	if !strings.Contains(string(cb), `$toolRule = printf "default(jwt.sub, \"\") == \"%s\" && default(mcp.tool.target, \"\") in [%s]" $sub`) {
		t.Error("the tool rule must be `<identity> && mcp.tool.target in <allow-list>` — mcp.tool.name on the " +
			"backend is the UNPREFIXED tool name, so a `<target>_` prefix match on it matches nothing")
	}
	// Comments may explain the trap; expressions must not fall into it.
	for _, e := range expressions {
		if strings.Contains(e, "mcp.tool.name") {
			t.Errorf("rule %q keys on mcp.tool.name: on the federated backend that is the unprefixed tool name, "+
				"so no target prefix ever matches", e)
		}
	}
	if strings.Contains(joined, "mcp.tool.name") {
		t.Error("the agent grant keys on mcp.tool.name: on the federated backend that is the unprefixed tool name")
	}
	if !strings.Contains(string(cb), "kind: AgentgatewayBackend\n                        name: adhar-mcp") {
		t.Error("the tool grant must target the federated MCP backend `adhar-mcp`; attached to the Gateway, " +
			"mcp.tool.* is never populated and the rule admits everything")
	}
}

type authzRule struct {
	Action string `json:"action"`
	Policy struct {
		MatchExpressions []string `json:"matchExpressions"`
	} `json:"policy"`
}
