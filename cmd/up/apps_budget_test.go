package up

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A cloud bring-up that is not given --apps-timeout must wait long enough for
// the identity chain; the local core keeps the flag's default; an explicit
// flag always wins, including 0.
func TestACloudBringUpWaitsForTheIdentityChainByDefault(t *testing.T) {
	flagDefault := 15 * time.Minute
	if got := effectiveAppsTimeout(flagDefault, false, true); got < 40*time.Minute {
		t.Errorf("cloud default apps budget is %s; the identity chain alone takes 20-25 min, so the strict verdict fails healthy bring-ups", got)
	}
	if got := effectiveAppsTimeout(flagDefault, false, false); got != flagDefault {
		t.Errorf("local keeps the flag default, got %s", got)
	}
	if got := effectiveAppsTimeout(7*time.Minute, true, true); got != 7*time.Minute {
		t.Errorf("an explicit flag must win on the cloud, got %s", got)
	}
	if got := effectiveAppsTimeout(0, true, true); got != 0 {
		t.Errorf("an explicit 0 (exit when the foundation is ready) must be honoured, got %s", got)
	}
	// The cloud path must actually use it, for both the watchdog and the controller.
	b, err := os.ReadFile("bootstrap.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "effectiveAppsTimeout(appsTimeout, appsTimeoutExplicit, true)") {
		t.Fatal("bootstrap.go does not derive the cloud apps budget through effectiveAppsTimeout")
	}
	if strings.Contains(src, "watchAppsBudget(bootstrapCtx, kubeClient, platformName, appsTimeout,") ||
		strings.Contains(src, "RunControllers(bootstrapCtx, mgr, exitCh, cancel, true, appsTimeout,") {
		t.Error("the cloud path still hands the raw flag to the watchdog or the controller")
	}
}

// A local bring-up runs with the flag's default; it must cover the local
// profile's identity chain (Keycloak, then the console), which measured 21 and
// 56 minutes on a laptop (2026-11-22). The cloud default is untouched.
func TestTheLocalAppsBudgetCoversTheLocalProfile(t *testing.T) {
	if got := effectiveAppsTimeout(localDefaultAppsTimeout, false, false); got < 30*time.Minute {
		t.Errorf("local apps budget = %s; the local profile's identity chain needs at least 30m", got)
	}
	if f := UpCmd.Flags().Lookup("apps-timeout"); f == nil || f.DefValue != localDefaultAppsTimeout.String() {
		t.Errorf("--apps-timeout default = %v; want localDefaultAppsTimeout (%s)", f, localDefaultAppsTimeout)
	}
	if got := effectiveAppsTimeout(localDefaultAppsTimeout, false, true); got != cloudDefaultAppsTimeout {
		t.Errorf("cloud apps budget = %s; changing the local default must not change cloud's %s", got, cloudDefaultAppsTimeout)
	}
}
