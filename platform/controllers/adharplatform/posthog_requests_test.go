package adharplatform

import (
	"regexp"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
)

// PostHog's requests must describe what PostHog actually holds.
//
// posthog-web and posthog-events each run at ~4.4 GiB working set and
// requested 2Gi; posthog-worker-default held ~730 MiB and requested nothing.
// The scheduler places by request, so all three landed on one node that read
// 46% requested and 90% used (888 MiB free), and everything scheduled there
// afterwards — Prometheus, a freshly built service — was OOM-killed or starved
// through its startup probe (2026-10-14). The symptom was "nodes keep crashing
// and apps become unhealthy"; the cause was three numbers that lied to the
// scheduler.
func TestPostHogRequestsReflectWhatItUses(t *testing.T) {
	body := readStackFile(t, "application/posthog/manifests/install.yaml")
	for name, floor := range map[string]string{
		"posthog-web":            "4Gi",
		"posthog-events":         "4Gi",
		"posthog-worker-default": "512Mi",
	} {
		got := deploymentMemoryRequest(t, body, name)
		if got == "" {
			t.Errorf("%s declares no memory request; it is invisible to the scheduler and lands on the fullest node", name)
			continue
		}
		have, min := resource.MustParse(got), resource.MustParse(floor)
		if have.Cmp(min) < 0 {
			t.Errorf("%s requests %s memory but holds more than %s at steady state; the scheduler "+
				"packs it beside other large pods and the node OOMs", name, got, floor)
		}
	}
}

// deploymentMemoryRequest returns the first container's memory request of the
// named Deployment in a multi-document manifest, or "" when it has none.
func deploymentMemoryRequest(t *testing.T, body, name string) string {
	t.Helper()
	for _, doc := range strings.Split(body, "\n---\n") {
		if !strings.Contains(doc, "kind: Deployment") {
			continue
		}
		if !regexp.MustCompile(`(?m)^metadata:\n(?:  .*\n)*?  name: ` + regexp.QuoteMeta(name) + `\n`).MatchString(doc) {
			continue
		}
		m := regexp.MustCompile(`(?m)^ +resources:\n(?: +limits:\n(?: +(?:cpu|memory): .*\n)*)? +requests:\n(?: +cpu: .*\n)? +memory: (\S+)`).FindStringSubmatch(doc)
		if m == nil {
			return ""
		}
		return m[1]
	}
	t.Fatalf("no Deployment named %s in the posthog manifest; update this guard", name)
	return ""
}
