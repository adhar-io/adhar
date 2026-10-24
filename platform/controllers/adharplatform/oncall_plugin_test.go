package adharplatform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The oncall chart provisions its bundled Grafana's plugin against its own
// test fixture (`helm-testing-oncall-engine`), a Service no install has, and
// leaves `grafanaUrl` unset so the plugin calls Grafana through the SSO proxy.
// Either one alone shows "Plugin not connected" (2026-10-24). post-render.py
// fixes both after every `helm template`; this pins the result.
func TestOnCallPluginIsWiredToItsRealServices(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "stack", "packages", "observability", "oncall", "manifests", "install.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{
		"onCallApiUrl: http://oncall-engine:8080\n",
		"grafanaUrl: http://oncall-grafana.adhar-system.svc.cluster.local\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the OnCall plugin provisioning lacks %q", strings.TrimSpace(want))
		}
	}
	if strings.Contains(s, "helm-testing-oncall-engine") {
		t.Error("the OnCall plugin still points at the chart's test-fixture engine")
	}
	if strings.Contains(s, "value: http://example.com") {
		t.Error("OnCall's BASE_URL is still the chart default; every integration webhook URL would point at example.com")
	}
	gen, _ := os.ReadFile(filepath.Join("..", "..", "stack", "packages", "observability", "oncall", "generate-manifests.sh"))
	if !strings.Contains(string(gen), "python3 post-render.py") {
		t.Error("generate-manifests.sh no longer runs post-render.py; the next regeneration reverts the fix")
	}
}
