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
