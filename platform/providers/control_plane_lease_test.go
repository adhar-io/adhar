package provider

import (
	"strings"
	"testing"
	"time"
)

// kube-controller-manager and kube-scheduler exit when they cannot renew their
// lease, and on a loaded control plane the upstream 10s deadline was lost every
// few minutes. The flags must land in each component's OWN manifest, after its
// own command line, and the renew deadline must stay generous.
func TestControlPlaneLeaseArgsTargetEachComponent(t *testing.T) {
	for _, c := range []string{"kube-controller-manager", "kube-scheduler"} {
		cmd := staticPodFlagCommand(c, "leader-elect-renew-deadline", "40s")
		for _, want := range []string{
			"/etc/kubernetes/manifests/" + c + ".yaml",
			"s|    - " + c + "|    - " + c + `\n    - --leader-elect-renew-deadline=40s|`,
			"grep -q -- '--leader-elect-renew-deadline='",
		} {
			if !strings.Contains(cmd, want) {
				t.Errorf("%s: command %q lacks %q", c, cmd, want)
			}
		}
	}
	d, err := time.ParseDuration(ControlPlaneLeaseArgs["leader-elect-renew-deadline"])
	if err != nil || d < 30*time.Second {
		t.Errorf("renew deadline %q: keep it >= 30s", ControlPlaneLeaseArgs["leader-elect-renew-deadline"])
	}
	l, _ := time.ParseDuration(ControlPlaneLeaseArgs["leader-elect-lease-duration"])
	if l <= d {
		t.Errorf("lease duration %s must exceed the renew deadline %s", l, d)
	}
}
