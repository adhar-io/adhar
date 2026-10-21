package up

import (
	"os"
	"strings"
	"testing"
)

// A cloud API server only trusts Keycloak if adhar configures it after
// Keycloak is up; without it Headlamp's sign-in "worked" and every API call
// returned 401 (2026-10-21). The step runs before the success panel and can
// only warn — the platform is usable without it.
func TestCloudUpTrustsKeycloakOnTheAPIServer(t *testing.T) {
	b, err := os.ReadFile("production.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	step := strings.Index(src, "trustKeycloakOnAPIServer(ctx, result, clusterName")
	panel := strings.Index(src, "printProductionSuccessMsg(environment, cfg.GlobalSettings.DefaultHost, clusterName)")
	if step < 0 || panel < 0 || step > panel {
		t.Fatal("a cloud adhar up no longer configures the API server's OIDC trust before reporting success")
	}
	fn := src[strings.Index(src, "func trustKeycloakOnAPIServer("):]
	fn = fn[:strings.Index(fn, "\n}\n")]
	if strings.Contains(fn, "return err") || strings.Contains(fn, "os.Exit") {
		t.Error("the OIDC step can fail the bring-up; it must only warn")
	}
	if !strings.Contains(fn, "adhar cluster oidc") {
		t.Error("the warning no longer tells the operator how to finish the step")
	}
}
