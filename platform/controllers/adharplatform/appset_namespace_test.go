package adharplatform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// The platform ApplicationSets must not ask Argo CD to create their namespace.
//
// Every element targets a namespace that already exists before any app syncs —
// `adhar-system`, created by the bootstrap controller, which also stamps the
// ADR-0023 placement label. So `CreateNamespace=true` could never create
// anything. What it DID do was add the namespace to each sync's first wave and
// make the sync wait for its health.
//
// Argo CD does not manage that namespace: `adhar up` creates it imperatively,
// so it carries no managedFields and its health is never computed. The wait
// therefore cannot be satisfied. Live on 2026-10-14:
//
//	26 of 79 Applications OutOfSync
//	10 stuck at "waiting for healthy state of /Namespace/adhar-system"
//	kafka-operator: "Operation terminated (retried 742 times)"
//
// The Kafka CR sits in wave 5, so the shared `adhar-kafka` cluster was never
// created and every Kafka-using service failed to start. Terminating the
// operation and re-syncing re-enters the loop — the option is the fault.
func TestThePlatformAppsetsDoNotWaitOnANamespaceTheyDoNotManage(t *testing.T) {
	for _, name := range []string{"adhar-appset-production.yaml", "adhar-appset-local.yaml"} {
		body := readStackRootFile(t, name)

		var doc struct {
			Spec struct {
				Template struct {
					Spec struct {
						Destination struct {
							Namespace string `json:"namespace"`
						} `json:"destination"`
						SyncPolicy struct {
							SyncOptions              []string       `json:"syncOptions"`
							ManagedNamespaceMetadata map[string]any `json:"managedNamespaceMetadata"`
						} `json:"syncPolicy"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		sp := doc.Spec.Template.Spec.SyncPolicy

		for _, o := range sp.SyncOptions {
			if strings.HasPrefix(o, "CreateNamespace") && strings.HasSuffix(o, "true") {
				t.Errorf("%s asks Argo CD to create its namespace; the namespace already exists and "+
					"is unmanaged by Argo CD, so every sync deadlocks waiting for a health it can "+
					"never compute", name)
			}
		}
		// managedNamespaceMetadata only applies when Argo CD creates the
		// namespace, so it cannot be kept once CreateNamespace is gone — and
		// the label it set is already applied by the bootstrap controller.
		if len(sp.ManagedNamespaceMetadata) > 0 {
			t.Errorf("%s still declares managedNamespaceMetadata, which only takes effect with "+
				"CreateNamespace=true", name)
		}

		// THE PRECONDITION. Dropping CreateNamespace is only safe while every
		// element targets a namespace something else creates. A new element
		// pointing somewhere else would silently fail to sync.
		for _, ns := range elementNamespaces(body) {
			if ns != "adhar-system" && ns != "default" {
				t.Errorf("%s has an element targeting namespace %q, which nothing guarantees "+
					"exists; either pre-create it or this appset needs CreateNamespace back", name, ns)
			}
		}
	}
}

// elementNamespaces returns every literal `namespace: "x"` in the generator
// elements — the per-package destinations.
func elementNamespaces(body string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		s := strings.TrimSpace(line)
		if !strings.HasPrefix(s, "namespace:") {
			continue
		}
		v := strings.Trim(strings.TrimSpace(strings.TrimPrefix(s, "namespace:")), `"'`)
		if v == "" || strings.Contains(v, "{{") || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func readStackRootFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(stackRoot(t), name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(b)
}

// A custom application runs only in its project's environment namespaces
// (dev/test/prod). The service-adoption ApplicationSet used to target a
// namespace named after the repo with CreateNamespace=true, which minted an
// empty `reactapp` namespace for every adopted app (2026-10-24). Its bootstrap
// Applications sync only the Application XR, which lives in adhar-system.
func TestAdoptedServicesNeverGetANamespaceOfTheirOwn(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(stackRoot(t), "adhar-services-appset.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var appset struct {
		Spec struct {
			Template struct {
				Spec struct {
					Destination struct {
						Namespace string `json:"namespace"`
					} `json:"destination"`
					SyncPolicy struct {
						SyncOptions              []string       `json:"syncOptions"`
						ManagedNamespaceMetadata map[string]any `json:"managedNamespaceMetadata"`
					} `json:"syncPolicy"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(b, &appset); err != nil {
		t.Fatal(err)
	}
	tpl := appset.Spec.Template.Spec
	if tpl.Destination.Namespace != "adhar-system" {
		t.Errorf("bootstrap Applications target %q; they must target the control-plane namespace adhar-system, never a per-service namespace", tpl.Destination.Namespace)
	}
	for _, o := range tpl.SyncPolicy.SyncOptions {
		if strings.HasPrefix(o, "CreateNamespace") && strings.HasSuffix(o, "true") {
			t.Errorf("bootstrap Applications must not create namespaces (found %q)", o)
		}
	}
	if tpl.SyncPolicy.ManagedNamespaceMetadata != nil {
		t.Errorf("managedNamespaceMetadata implies a namespace this Application owns; it must not own one")
	}
}
