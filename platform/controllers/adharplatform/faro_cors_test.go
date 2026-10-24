package adharplatform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Browsers send Faro telemetry cross-origin, and the Faro Web SDK adds an
// Idempotency-Key header Alloy's faro.receiver does not allow in its CORS
// preflight (no setting extends that list). Without faro-cors in front every
// frontend's real-user telemetry was refused by the browser (2026-10-24).
func TestBrowserTelemetryPassesTheCORSPreflight(t *testing.T) {
	dir := filepath.Join("..", "..", "stack", "packages", "observability", "faro", "manifests")
	route, _ := os.ReadFile(filepath.Join(dir, "httproute.yaml"))
	if !strings.Contains(string(route), "- name: faro-cors\n          port: 80") {
		t.Error("the faro route must go through faro-cors, not straight to the receiver")
	}
	cors, err := os.ReadFile(filepath.Join(dir, "cors.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"add_header Access-Control-Allow-Headers $http_access_control_request_headers always;",
		"proxy_hide_header Access-Control-Allow-Origin;",
		"proxy_pass http://faro-alloy.adhar-system.svc.cluster.local:12347;",
	} {
		if !strings.Contains(string(cors), want) {
			t.Errorf("faro-cors lacks %q", want)
		}
	}
}
