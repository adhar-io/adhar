package adharplatform

import (
	"regexp"
	"strings"
	"testing"
)

// Prometheus must declare the memory it needs, in the generator values AND in
// the rendered manifest.
//
// The chart default is `resources: {}`. With no request the scheduler placed
// Prometheus by request arithmetic — 46% requested — on a node whose REAL
// memory use was 90%, and the kernel OOM-killed it during WAL replay 27 times
// in a row (2026-10-14, exit 137, `OOMKilled`). While it loops nothing on the
// platform has metrics: the console's panels, Grafana, the autoscaler's
// utilisation reads and every `cnpg_*` series are gone, and the symptom reads
// like a Prometheus bug rather than a scheduling one.
//
// Both halves are pinned because this package is GENERATED: a fix made only in
// install.yaml is reverted by the next `generate-manifests.sh`, and one made
// only in values.yaml never reaches a cluster until someone regenerates.
func TestPrometheusDeclaresTheMemoryItNeeds(t *testing.T) {
	values := readStackFile(t, "observability/kube-prometheus/values.yaml")
	spec := sectionAfter(t, values, "\n  prometheusSpec:\n", true)
	vm := regexp.MustCompile(`(?m)^    resources:\n(?:.*\n)*?      requests:\n(?:.*\n)*?        memory: (\S+)`).FindStringSubmatch(spec)
	if vm == nil {
		t.Fatal("values.yaml prometheus.prometheusSpec.resources declares no memory request; " +
			"Prometheus is scheduled onto nodes with no room and OOM-killed during WAL replay")
	}

	install := readStackFile(t, "observability/kube-prometheus/manifests/install.yaml")
	cr := sectionAfter(t, install, "\nkind: Prometheus\n", false)
	im := regexp.MustCompile(`(?m)^  resources:\n(?:.*\n)*?    requests:\n(?:.*\n)*?      memory: (\S+)`).FindStringSubmatch(cr)
	if im == nil {
		t.Fatal("the rendered Prometheus CR in install.yaml declares no memory request; the values " +
			"file was changed without regenerating, so no cluster gets the fix")
	}
	if vm[1] != im[1] {
		t.Errorf("Prometheus memory request drifted: values.yaml says %s, install.yaml says %s", vm[1], im[1])
	}
}

// sectionAfter returns the text from marker to the end of its block: the next
// `---` document break, and — for a values file, which has none — the next
// top-level key. Without the cut a regex matches a sibling's resources block.
func sectionAfter(t *testing.T, body, marker string, topLevelKeysEnd bool) string {
	t.Helper()
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("marker %q not found", strings.TrimSpace(marker))
	}
	rest := body[i+len(marker):]
	if j := strings.Index(rest, "\n---\n"); j >= 0 {
		rest = rest[:j]
	}
	if topLevelKeysEnd {
		if j := regexp.MustCompile(`(?m)^[a-zA-Z]`).FindStringIndex(rest); j != nil {
			rest = rest[:j[0]]
		}
	}
	return rest
}
