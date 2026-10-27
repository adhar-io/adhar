package adharplatform

import (
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// The nightly knowledge reindex is the runtime's own `adhar-ai index`, so it
// must index into the stores the runtime SEARCHES, as an identity the AI
// gateway admits. It ran for weeks with neither: no ADHAR_AI_VECTOR_URL (it fell
// back to a pgvector table whose type the database no longer installs) and no
// bearer (401 on every embedding), failing three attempts a night (GCP,
// 2026-10-26). Pinned against the runtime Deployment, variable for variable.
func TestKnowledgeReindexIndexesWhereTheRuntimeSearches(t *testing.T) {
	job := workloadContainer(t, "ai/adhar-ai/manifests/knowledge-reindex.yaml", "CronJob", "reindex")
	runtime := workloadContainer(t, "ai/adhar-ai/manifests/agent-runtime.yaml", "Deployment", "runtime")

	// A release, never :latest: nobody watches the 02:15 run.
	image, _ := job["image"].(string)
	if !regexp.MustCompile(`^ghcr\.io/adhar-io/adhar-ai-runtime:[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(image) {
		t.Errorf("knowledge-reindex runs %q; pin a released adhar-ai-runtime version", image)
	}
	if job["imagePullPolicy"] == "Always" {
		t.Error("knowledge-reindex pulls Always; a pinned release needs no re-pull")
	}

	jobEnv, runtimeEnv := envWithSecretRefs(job), envWithSecretRefs(runtime)
	for _, name := range []string{
		"LLM_GATEWAY_URL",
		"OIDC_ISSUER_URL", "OIDC_CLIENT_ID", "ADHAR_AI_OIDC_TOKEN_URL", "ADHAR_AI_OIDC_CLIENT_SECRET",
		"RAG_DB_HOST", "RAG_DB_NAME",
		"ADHAR_AI_VECTOR_URL", "ADHAR_AI_VECTOR_COLLECTION",
		"ADHAR_AI_GRAPH_URI", "ADHAR_AI_GRAPH_DATABASE", "ADHAR_AI_GRAPH_USERNAME", "ADHAR_AI_GRAPH_PASSWORD",
	} {
		got, ok := jobEnv[name]
		if !ok {
			t.Errorf("knowledge-reindex does not set %s", name)
			continue
		}
		if want := runtimeEnv[name]; got != want {
			t.Errorf("knowledge-reindex %s = %s, the runtime has %s", name, got, want)
		}
	}
	if got := jobEnv["ADHAR_AI_OIDC_CLIENT_SECRET"]; got != "secret:keycloak-clients/ADHAR_AI_CLIENT_SECRET" {
		t.Errorf("knowledge-reindex mints its gateway token with %s, not the adhar-ai client's secret", got)
	}

	// The Qdrant key comes from the Secret the Vector composition writes:
	// `<vector name>-api-key`, its status.credentialsSecret.
	var vector string
	for _, d := range docsOf(t, "ai/adhar-ai/manifests/stores.yaml") {
		if d["kind"] == "Vector" {
			vector = toString(sgDig(d, "metadata", "name"))
		}
	}
	if vector == "" {
		t.Fatal("no Vector in ai/adhar-ai/manifests/stores.yaml")
	}
	if !strings.Contains(readConfigurationFile(t, "compositions", "vector", "qdrant.yaml"), "credentialsSecret: {{ $name }}-api-key") {
		t.Fatal("the Vector composition no longer names its connection Secret <name>-api-key; update this test")
	}
	if got, want := jobEnv["ADHAR_AI_VECTOR_API_KEY"], "secret:"+vector+"-api-key/api-key"; got != want {
		t.Errorf("knowledge-reindex reads the Qdrant key from %s; the composition writes %s", got, want)
	}
	if !strings.Contains(jobEnv["ADHAR_AI_VECTOR_URL"], "//"+vector+".adhar-system.svc") {
		t.Errorf("knowledge-reindex ADHAR_AI_VECTOR_URL = %s does not point at the %s Vector", jobEnv["ADHAR_AI_VECTOR_URL"], vector)
	}
}

// workloadContainer returns the named container of the first workload of the kind.
func workloadContainer(t *testing.T, rel, kind, name string) map[string]any {
	t.Helper()
	for _, doc := range strings.Split(readStackFile(t, rel), "\n---") {
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil || obj == nil || obj["kind"] != kind {
			continue
		}
		for _, c := range asList(podSpecOf(obj)["containers"]) {
			if ctr, _ := c.(map[string]any); ctr["name"] == name {
				return ctr
			}
		}
	}
	t.Fatalf("%s: no %s container %q", rel, kind, name)
	return nil
}

// envWithSecretRefs renders each env entry as its literal value, or `secret:<name>/<key>`.
func envWithSecretRefs(ctr map[string]any) map[string]string {
	out := map[string]string{}
	for _, e := range asList(ctr["env"]) {
		em, _ := e.(map[string]any)
		name, _ := em["name"].(string)
		if v, ok := em["value"].(string); ok {
			out[name] = v
			continue
		}
		ref, _ := dig(em, "valueFrom", "secretKeyRef").(map[string]any)
		out[name] = "secret:" + toString(ref["name"]) + "/" + toString(ref["key"])
	}
	return out
}
