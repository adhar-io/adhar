package adharplatform

// The two things a Crossplane chart regeneration silently reverts.
//
// Both of these were hand-edits of the generated manifests, and they survived
// for months only because nobody re-ran the generator. The v2.3.1 → v2.4.2 bump
// on 2026-10-09 re-rendered all three files and took both away without a word —
// the same shape as the plane package's `timeoutSeconds` revert. They now live
// in hack/crossplane/{generate-manifests.sh,values*.yaml}; this is the guard
// that notices if a future bump drops them again.
//
// Only the EMBEDDED manifests are checked. The GitOps copy under
// platform/stack/packages/infrastructure/crossplane/ is a parity-only rendering
// that must never be installed alongside the bootstrap one (see the REDUNDANCY
// NOTICE in its generator), and it has never carried either edit.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// crossplaneManifests are the files the controller embeds and applies.
func crossplaneManifests(t *testing.T) map[string]string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, name := range []string{"install.yaml", "install-ha.yaml"} {
		path := filepath.Join(wd, "resources", "crossplane", name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		out[name] = string(raw)
	}
	return out
}

// deployments returns every Deployment in a multi-document manifest, keyed by
// name.
func deployments(t *testing.T, manifest string) map[string]map[string]any {
	t.Helper()
	found := map[string]map[string]any{}
	dec := yaml.NewDecoder(strings.NewReader(manifest))
	for {
		var doc map[string]any
		err := dec.Decode(&doc)
		if err != nil {
			break
		}
		if doc == nil || doc["kind"] != "Deployment" {
			continue
		}
		meta, _ := doc["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		found[name] = doc
	}
	return found
}

// podSpec digs out spec.template.spec.
func podSpec(t *testing.T, deploy map[string]any) map[string]any {
	t.Helper()
	spec, _ := deploy["spec"].(map[string]any)
	tmpl, _ := spec["template"].(map[string]any)
	ps, _ := tmpl["spec"].(map[string]any)
	if ps == nil {
		t.Fatal("deployment has no spec.template.spec")
	}
	return ps
}

// Every platform component shares the adhar-system namespace (ADR-0011), so
// Kubernetes injects a <SVC>_PORT env var per Service in it. cosign's
// policy-controller ships a Service named "webhook", which arrives as
// WEBHOOK_PORT=tcp://<ip>:443 — and crossplane reads that as its own
// --webhook-port flag and refuses to start. With the core Deployment down, no
// package, XRD or Composition in the cluster is ever reconciled.
//
// Named by hack/crossplane/generate-manifests.sh, which re-applies the field
// after every `helm template`.
func TestCrossplaneManifestsDisableServiceLinks(t *testing.T) {
	for name, manifest := range crossplaneManifests(t) {
		found := deployments(t, manifest)
		if len(found) != 2 {
			t.Fatalf("%s: expected the crossplane and rbac-manager Deployments, found %d: %v",
				name, len(found), keysOf(found))
		}
		for _, want := range []string{"crossplane", "crossplane-rbac-manager"} {
			deploy, ok := found[want]
			if !ok {
				t.Fatalf("%s: no Deployment named %q (found %v) — the chart renamed it and the "+
					"generator's anchor needs updating with it", name, want, keysOf(found))
			}
			links, present := podSpec(t, deploy)["enableServiceLinks"]
			if !present {
				t.Errorf("%s: Deployment/%s does not set enableServiceLinks; cosign's Service/webhook "+
					"becomes WEBHOOK_PORT=tcp://… which crossplane parses as --webhook-port and dies. "+
					"Re-apply it in hack/crossplane/generate-manifests.sh rather than by hand",
					name, want)
				continue
			}
			if links != false {
				t.Errorf("%s: Deployment/%s sets enableServiceLinks: %v, want false", name, want, links)
			}
		}
	}
}

// The limits exist because crossplane was throttled and OOM-killed in a loop
// (29 restarts on a 10-node cloud cluster) at the chart defaults once the cloud
// provider packages registered their CRDs. They were hand-raised in the
// generated files until the 2.4.2 bump reverted them; they now come from
// hack/crossplane/values*.yaml, so a regeneration keeps them — but a values
// edit could still take them away, and the symptom is a control plane that
// stops reconciling under load rather than an error.
func TestCrossplaneCoreKeepsItsRaisedLimits(t *testing.T) {
	for name, manifest := range crossplaneManifests(t) {
		deploy, ok := deployments(t, manifest)["crossplane"]
		if !ok {
			t.Fatalf("%s: no Deployment named crossplane", name)
		}
		containers, _ := podSpec(t, deploy)["containers"].([]any)
		if len(containers) == 0 {
			t.Fatalf("%s: crossplane Deployment has no containers", name)
		}
		for _, c := range containers {
			container, _ := c.(map[string]any)
			cname, _ := container["name"].(string)
			resources, _ := container["resources"].(map[string]any)
			limits, _ := resources["limits"].(map[string]any)
			if limits == nil {
				t.Errorf("%s: container %s has no resource limits", name, cname)
				continue
			}
			if got := limits["cpu"]; got != "2" {
				t.Errorf("%s: container %s cpu limit is %v, want \"2\" (set in hack/crossplane/values.yaml; "+
					"the chart default of 200m throttled the core until nothing reconciled)", name, cname, got)
			}
			if got := limits["memory"]; got != "4Gi" {
				t.Errorf("%s: container %s memory limit is %v, want 4Gi (set in hack/crossplane/values.yaml; "+
					"the chart default of 512Mi OOM-killed the core in a loop)", name, cname, got)
			}
		}
	}
}

// And the note that says those numbers are caps, not reservations, and that
// they are not to be patched in the generated file. Prepended by the generator.
func TestCrossplaneManifestsExplainTheirLimits(t *testing.T) {
	for name, manifest := range crossplaneManifests(t) {
		if !strings.HasPrefix(manifest, "# NOTE: container resource") {
			t.Errorf("%s: lost the header explaining that the limits are caps and live in "+
				"hack/crossplane/values*.yaml; the generator prepends it", name)
		}
	}
}

func keysOf(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
