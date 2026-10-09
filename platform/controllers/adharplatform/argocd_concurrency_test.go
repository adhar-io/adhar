package adharplatform

// The HA and non-HA Argo CD renderings must agree on apply concurrency.
//
// Two renderings of one component drift unless every knob is set in BOTH, and
// this component has now drifted twice in opposite directions:
//
//   timeout.reconciliation was tuned only in install-ha.yaml, leaving the common
//   non-HA path at 120s+15s so the slow path was the default one (fixed
//   2026-09-30).
//
//   apply concurrency was tuned only in install.yaml. install-ha.yaml did not set
//   controller.kubectl.parallelism.limit at all — the env var is a
//   configMapKeyRef with `optional: true`, so an absent key silently falls
//   through to Argo CD's own default of 20, five times the value the non-HA path
//   was deliberately pinned to — and operation.processors sat at the upstream 10.
//
// Why that matters, measured: enableHAMode does not make the API server bigger.
// On Civo's managed k3s the control plane runs on a pool node, so HA adds
// replicas of Argo CD, redis and CNPG and gives the apiserver nothing but load.
// Three Civo clusters in a row (2026-10-09) lost their API server within ~1-5
// minutes of Argo CD starting to reconcile 77 Applications — host answering ICMP
// at 62 ms, 6443 closed, provider still reporting ACTIVE/ready=true. Doubling the
// node size (4 vCPU to 8) bought ~5 minutes and did not prevent it, which is the
// tell that the pressure is request concurrency rather than CPU per node.
//
// The guard deliberately compares the two files rather than asserting fixed
// numbers: the numbers may legitimately change, but never in only one of them.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// concurrencyKeys are the settings that bound how hard Argo CD hits the API
// server. Every one of them must be present, and equal, in both renderings.
var concurrencyKeys = []string{
	"controller.kubectl.parallelism.limit",
	"controller.operation.processors",
	"controller.status.processors",
}

// argocdCmdParams reads argocd-cmd-params-cm out of one rendering.
func argocdCmdParams(t *testing.T, file string) map[string]string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(wd, "resources", "argocd", file)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	// Per-document parsing, NOT a streaming yaml.Decoder.
	//
	// A Decoder stops at the first document it cannot unmarshal, and these files
	// are the full upstream install — 34k lines, including CRDs whose `data` is
	// not a map[string]string. The scan then ended long before reaching
	// argocd-cmd-params-cm and the guard reported the ConfigMap as missing from a
	// file that plainly contains it. Splitting and trying each document
	// independently means one unparseable CRD cannot hide the thing being checked.
	for _, chunk := range strings.Split(string(raw), "\n---") {
		var doc struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Data map[string]string `yaml:"data"`
		}
		if err := yaml.Unmarshal([]byte(chunk), &doc); err != nil {
			continue
		}
		if doc.Kind == "ConfigMap" && doc.Metadata.Name == "argocd-cmd-params-cm" {
			return doc.Data
		}
	}
	t.Fatalf("%s has no argocd-cmd-params-cm ConfigMap", file)
	return nil
}

func TestHAAndNonHAAgreeOnApplyConcurrency(t *testing.T) {
	plain := argocdCmdParams(t, "install.yaml")
	ha := argocdCmdParams(t, "install-ha.yaml")

	for _, key := range concurrencyKeys {
		p, okP := plain[key]
		h, okH := ha[key]

		// Absent is the dangerous case: the env var is an optional
		// configMapKeyRef, so a missing key is not an error — it silently
		// becomes Argo CD's upstream default.
		if !okP {
			t.Errorf("install.yaml does not set %s; an absent key falls through to Argo CD's "+
				"upstream default rather than the value this platform tuned", key)
		}
		if !okH {
			t.Errorf("install-ha.yaml does not set %s; an absent key falls through to Argo CD's "+
				"upstream default (20 for kubectl.parallelism.limit), so enabling HA mode silently "+
				"multiplies the load on the API server", key)
		}
		if okP && okH && p != h {
			t.Errorf("%s is %q in install.yaml but %q in install-ha.yaml. enableHAMode does not make "+
				"the API server bigger — on a managed k3s the control plane is a pool node — so the "+
				"two renderings must not differ on how hard Argo CD applies", key, p, h)
		}
	}
}

// And the tuned value must stay conservative. This is the one absolute
// assertion, because the failure it prevents is a dead API server rather than a
// slow one: raising parallelism to 16 made a local bring-up abort at 6 of 17
// apps with `context deadline exceeded` on a plain GET (2026-09-30).
func TestApplyParallelismStaysConservative(t *testing.T) {
	for _, file := range []string{"install.yaml", "install-ha.yaml"} {
		data := argocdCmdParams(t, file)
		if got := data["controller.kubectl.parallelism.limit"]; got != "4" {
			t.Errorf("%s: controller.kubectl.parallelism.limit = %q, want \"4\". Raising it needs to "+
				"become provider-aware first (a field on BuildCustomizationSpec) — a managed cloud "+
				"control plane can take more, a single-node Kind or a 3-node k3s cannot", file, got)
		}
	}
}
