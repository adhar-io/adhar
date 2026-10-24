package adharplatform

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

type pkgObject struct {
	file, kind, namespace, name string
	wave                        int
	readsKeycloakClients        bool
}

// packageObjects reads every manifest directory an ApplicationSet element
// points at, keyed by that directory (one Argo CD Application each).
func packageObjects(t *testing.T) map[string][]pkgObject {
	t.Helper()
	root := stackRoot(t)
	out := map[string][]pkgObject{}
	_ = filepath.WalkDir(filepath.Join(root, "packages"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !(strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yaml.tmpl")) {
			return nil
		}
		dir := filepath.Dir(path)
		if filepath.Base(dir) != "manifests" {
			return nil
		}
		raw, _ := os.ReadFile(path)
		for _, doc := range strings.Split(string(raw), "\n---") {
			var o struct {
				Kind     string `json:"kind"`
				Metadata struct {
					Name        string            `json:"name"`
					Namespace   string            `json:"namespace"`
					Annotations map[string]string `json:"annotations"`
				} `json:"metadata"`
			}
			if yaml.Unmarshal([]byte(doc), &o) != nil || o.Kind == "" || o.Metadata.Name == "" || strings.Contains(o.Metadata.Name, "{{") {
				continue
			}
			w, _ := strconv.Atoi(o.Metadata.Annotations["argocd.argoproj.io/sync-wave"])
			if strings.Contains(o.Metadata.Annotations["helm.sh/hook"], "test") {
				continue
			}
			out[dir] = append(out[dir], pkgObject{
				file: filepath.Base(path), kind: o.Kind, namespace: o.Metadata.Namespace, name: o.Metadata.Name, wave: w,
				readsKeycloakClients: o.Kind == "ExternalSecret" && strings.Contains(doc, "key: keycloak-clients"),
			})
		}
		return nil
	})
	return out
}

// Two copies of one object in one Application is Argo CD's
// RepeatedResourceWarning and two waves owning it. Litmus shipped the
// chaosexperiments CRD twice — once from litmus-core, once from the
// fault-catalogue chart under --include-crds.
//
// Checked for every package some profile enables. Known exceptions are listed
// with their reason, so a NEW duplicate still fails.
func TestNoPackageDefinesAnObjectTwice(t *testing.T) {
	known := map[string]string{
		// Tekton Pipelines and Tekton Triggers each ship these, in what were
		// separate namespaces upstream; under ADR-0011 both land in
		// adhar-system. config-logging is identical; config-observability is
		// NOT, so one component runs with the other's metrics settings. Open:
		// render one merged copy in tekton's generator.
		"application/tekton/manifests ConfigMap/adhar-system/config-logging":       "tekton",
		"application/tekton/manifests ConfigMap/adhar-system/config-observability": "tekton",
	}
	enabled := map[string]bool{}
	for _, appset := range []string{"adhar-appset-local.yaml", "adhar-appset-production.yaml"} {
		raw, err := os.ReadFile(filepath.Join(stackRoot(t), appset))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Spec struct {
				Generators []struct {
					List struct {
						Elements []map[string]string `json:"elements"`
					} `json:"list"`
				} `json:"generators"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		for _, g := range doc.Spec.Generators {
			for _, e := range g.List.Elements {
				if e["enabled"] == "true" {
					enabled[e["manifestPath"]] = true
				}
			}
		}
	}
	for dir, objs := range packageObjects(t) {
		rel, _ := filepath.Rel(filepath.Join(stackRoot(t), "packages"), dir)
		if !enabled[rel] {
			continue
		}
		seen := map[string]string{}
		for _, o := range objs {
			key := o.kind + "/" + o.namespace + "/" + o.name
			if prev, dup := seen[key]; dup && known[rel+" "+key] == "" {
				t.Errorf("%s: %s is defined in both %s and %s", rel, key, prev, o.file)
			}
			seen[key] = o.file
		}
	}
}

// A package's `<pkg>-keycloak-client` ConfigMap is what makes the keycloak
// package create its client and publish its keys into keycloak-clients. An
// ExternalSecret reading keycloak-clients in an EARLIER wave of the same
// Application waits for keys only a later wave can cause: Argo CD never
// reaches that wave, and the package deadlocks on a fresh cluster (litmus,
// GCP 2026-10-24: 147 retries, no CRD ever applied).
func TestKeycloakClientSecretsWaitForTheirClient(t *testing.T) {
	for dir, objs := range packageObjects(t) {
		clientWave, hasClient := 0, false
		for _, o := range objs {
			if o.kind == "ConfigMap" && strings.HasSuffix(o.name, "-keycloak-client") {
				clientWave, hasClient = o.wave, true
			}
		}
		if !hasClient {
			continue
		}
		for _, o := range objs {
			if o.readsKeycloakClients && o.wave < clientWave {
				rel, _ := filepath.Rel(stackRoot(t), dir)
				t.Errorf("%s: ExternalSecret %s (%s) is at wave %d, before the package's keycloak client ConfigMap (wave %d) that publishes the keys it reads",
					rel, o.name, o.file, o.wave, clientWave)
			}
		}
	}
}
