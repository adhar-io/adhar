package get

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func agentObj(name, env, project string, models []string, ready bool) unstructured.Unstructured {
	var ms []interface{}
	for _, m := range models {
		ms = append(ms, m)
	}
	obj := map[string]interface{}{
		"apiVersion": "platform.adhar.io/v1alpha1", "kind": "AgentWorkload",
		"metadata": map[string]interface{}{"name": name, "namespace": "adhar-system"},
		"spec": map[string]interface{}{"parameters": map[string]interface{}{
			"name": name, "environment": env, "project": project,
			"models": map[string]interface{}{"allow": ms},
		}},
		"status": map[string]interface{}{"namespace": name + "-" + env},
	}
	if ready {
		obj["status"].(map[string]interface{})["conditions"] = []interface{}{map[string]interface{}{"type": "Ready", "status": "True"}}
	}
	return unstructured.Unstructured{Object: obj}
}

// The listing is the operator's inventory of agents: deterministic order
// (project, then name), the XRD's defaults filled in where a spec omits them,
// and a name filter that matches either the parameter or the object name.
func TestAgentsFromListOrdersDefaultsAndFilters(t *testing.T) {
	items := []unstructured.Unstructured{
		agentObj("zeta", "prod", "shop", []string{"claude-.*", "local/.*", "openai/.*"}, true),
		agentObj("alpha", "dev", "shop", nil, false),
		agentObj("smoke", "dev", "", []string{"claude-.*"}, true),
	}
	got := agentsFromList(items, nil)
	if len(got) != 3 {
		t.Fatalf("got %d agents, want 3", len(got))
	}
	if got[0].Name != "smoke" || got[0].Project != "adhar" || got[0].Team != "platform" || got[0].TokensHour != 100000 {
		t.Errorf("an agent without project/team/budget must show the XRD defaults (adhar/platform/100000); got %+v", got[0])
	}
	if got[1].Name != "alpha" || got[2].Name != "zeta" {
		t.Errorf("agents must sort by project then name; got %s, %s", got[1].Name, got[2].Name)
	}
	if !got[2].Ready || got[1].Ready {
		t.Error("readiness must come from the Ready condition")
	}
	if got[2].Namespace != "zeta-prod" {
		t.Errorf("namespace must come from status (or <name>-<env>); got %q", got[2].Namespace)
	}
	if only := agentsFromList(items, []string{"alpha"}); len(only) != 1 || only[0].Name != "alpha" {
		t.Errorf("name filter returned %+v", only)
	}
	if s := summarizeList(got[2].Models); s != "claude-.*,local/.*,+1" {
		t.Errorf("summarizeList = %q", s)
	}
	if s := summarizeList(nil); s != "none" {
		t.Errorf("an empty allow-list must read `none` (the gateway denies everything), got %q", s)
	}
}
