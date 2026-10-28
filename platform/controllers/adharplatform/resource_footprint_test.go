package adharplatform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"
)

// The platform runs ONE copy of every component by default.
//
// Four charts shipped their HA defaults (dapr-scheduler x3, oncall's redis
// replicas x3, falcosidekick x2, argo-rollouts x2) into a profile whose
// clusters are 3-7 nodes; HA is a per-cluster choice (`enableHAMode`), not a
// chart default to inherit. Every extra replica is memory the scheduler must
// find on a cluster that was already at 90% real usage (2026-10-14).
func TestNoPackageWorkloadShipsMoreThanOneReplica(t *testing.T) {
	forEachPackageWorkload(t, func(pkg, file, kind, name string, spec map[string]any) {
		if kind == "DaemonSet" {
			return
		}
		if r, ok := spec["replicas"].(float64); ok && r > 1 {
			t.Errorf("%s: %s/%s ships replicas=%d; one copy by default, HA is the cluster's choice", pkg, kind, name, int(r))
		}
	})
}

// No single container requests more than 1 GiB unless the process is known to
// hold that much. The chart defaults that went past it were a 4 GiB request
// for coderd (idles at ~200 MiB) and 1 GiB for the kpack controller (a
// reconciler) — requests the scheduler reserved and nothing used.
func TestLargeMemoryRequestsAreJustifiedByMeasurement(t *testing.T) {
	ceiling := resource.MustParse("1Gi")
	// container -> why it legitimately holds more than 1 GiB
	justified := map[string]string{
		"posthog-web":     "measured 4.4 GiB working set (Django + plugins); the knob to cut it is PostHog's, not the request",
		"posthog-events":  "measured 4.4 GiB working set (plugin-server heap)",
		"posthog-workers": "parent + RedBeat scheduler hold 1.97 GiB before any child runs; 2.0-2.7 GiB working, 2.93 GiB peak (Azure, 2026-10-28)",
		"nexus":           "Sonatype's floor is ~2.7 GiB heap+direct; a 1 GiB heap starved boot",
		"penpot-backend":  "measured 1.5-1.6 GiB working set (JVM); at the 64Mi default it was evicted 5x on 2026-10-22",
		"trivy-operator":  "informer caches measured 1.1 GiB steady, 1.7 GiB peak (2026-10-22)",
	}
	forEachPackageWorkload(t, func(pkg, file, kind, name string, spec map[string]any) {
		for _, c := range containersOf(spec) {
			req, _ := dig(c, "resources", "requests", "memory").(string)
			if req == "" {
				continue
			}
			q, err := resource.ParseQuantity(req)
			if err != nil {
				continue
			}
			if q.Cmp(ceiling) > 0 {
				cname, _ := c["name"].(string)
				if _, ok := justified[cname]; !ok {
					t.Errorf("%s: %s/%s container %q requests %s memory with no measured reason recorded in this test", pkg, kind, name, cname, req)
				}
			}
		}
	})
}

// The agent is the one that holds ~1 GiB per node; the reloader is ~10 MiB.
// On 2026-10-14 the measured request sat on config-reloader, so the agent kept
// running unaccounted while a 768Mi reservation protected a 10 MiB sidecar.
func TestAlloyRequestsBelongToTheAgent(t *testing.T) {
	body := readStackFile(t, "observability/alloy/manifests/install.yaml")
	for _, doc := range strings.Split(body, "\n---\n") {
		if !strings.Contains(doc, "kind: DaemonSet") {
			continue
		}
		var ds map[string]any
		if err := yaml.Unmarshal([]byte(doc), &ds); err != nil {
			t.Fatal(err)
		}
		spec, _ := ds["spec"].(map[string]any)
		for _, c := range containersOf(spec) {
			name, _ := c["name"].(string)
			req, _ := dig(c, "resources", "requests", "memory").(string)
			switch name {
			case "alloy":
				if q := resource.MustParse(nonEmpty(req, "0")); q.Cmp(resource.MustParse("512Mi")) < 0 {
					t.Errorf("the alloy agent requests %q; it was measured at 980-1120Mi per node", req)
				}
			case "config-reloader":
				if q := resource.MustParse(nonEmpty(req, "0")); q.Cmp(resource.MustParse("128Mi")) > 0 {
					t.Errorf("config-reloader requests %q; it is a 10 MiB sidecar — the agent's request has landed on the wrong container", req)
				}
			}
		}
	}
}

// Every container in the platform namespace gets a baseline request, so the
// scheduler counts the ~100 containers whose packages declare none. Requests
// only: a default limit would OOM-kill the variable-footprint processes those
// packages left unlimited on purpose.
func TestThePlatformNamespaceGivesSilentContainersARequest(t *testing.T) {
	body := readStackFile(t, "security/adhar-tenant-quotas/manifests/platform-defaults.yaml")
	var lr map[string]any
	if err := yaml.Unmarshal([]byte(body), &lr); err != nil {
		t.Fatal(err)
	}
	if lr["kind"] != "LimitRange" || dig(lr, "metadata", "namespace") != "adhar-system" {
		t.Fatal("platform-defaults.yaml is not a LimitRange in adhar-system")
	}
	limits, _ := dig(lr, "spec", "limits").([]any)
	if len(limits) == 0 {
		t.Fatal("the LimitRange has no limits entry")
	}
	entry, _ := limits[0].(map[string]any)
	if dig(entry, "defaultRequest", "memory") == nil || dig(entry, "defaultRequest", "cpu") == nil {
		t.Error("the LimitRange sets no defaultRequest for cpu+memory; silent containers stay invisible to the scheduler")
	}
	if entry["default"] != nil || entry["max"] != nil {
		t.Error("the LimitRange sets a default limit or a max; that OOM-kills or rejects the processes that deliberately run unlimited")
	}
}

/* ───── helpers ───── */

func forEachPackageWorkload(t *testing.T, fn func(pkg, file, kind, name string, spec map[string]any)) {
	t.Helper()
	appset, err := os.ReadFile(filepath.Join(stackPackagesDir(t), "..", "adhar-appset-production.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(appset, &doc); err != nil {
		t.Fatal(err)
	}
	gens, _ := dig(doc, "spec", "generators").([]any)
	seen := 0
	for _, g := range gens {
		elements, _ := dig(g.(map[string]any), "list", "elements").([]any)
		for _, el := range elements {
			e := el.(map[string]any)
			if e["enabled"] != "true" {
				continue
			}
			pkg, _ := e["name"].(string)
			path, _ := e["manifestPath"].(string)
			if path == "" {
				path, _ = e["path"].(string)
			}
			dir := filepath.Join(stackPackagesDir(t), path)
			_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() || !(strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".yaml.tmpl")) {
					return nil
				}
				raw, _ := os.ReadFile(p)
				for _, part := range strings.Split(string(raw), "\n---\n") {
					if !strings.Contains(part, "kind: ") {
						continue
					}
					var obj map[string]any
					if yaml.Unmarshal([]byte(part), &obj) != nil {
						continue
					}
					kind, _ := obj["kind"].(string)
					if kind != "Deployment" && kind != "StatefulSet" && kind != "DaemonSet" {
						continue
					}
					name, _ := dig(obj, "metadata", "name").(string)
					spec, _ := obj["spec"].(map[string]any)
					seen++
					fn(pkg, p, kind, name, spec)
				}
				return nil
			})
		}
	}
	if seen < 100 {
		t.Fatalf("walked only %d workloads; the production profile has well over 100 — the walker is broken", seen)
	}
}

func containersOf(spec map[string]any) []map[string]any {
	raw, _ := dig(spec, "template", "spec", "containers").([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, c := range raw {
		if m, ok := c.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
