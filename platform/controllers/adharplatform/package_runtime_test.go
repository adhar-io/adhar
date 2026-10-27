package adharplatform

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Every Crossplane package runtime — each provider and each Function — runs in
// adhar-system beside every other platform Service (ADR-0011), so Kubernetes
// injects a <SVC>_PORT variable per Service into it. Knative's Service/webhook
// becomes WEBHOOK_PORT=tcp://<ip>:9090, the upjet providers read that as their
// webhook port and exit, and Crossplane reports the providers unhealthy: on
// 2026-10-26 all four GCP provider pods had crash-looped 410 times on
// `strconv.ParseFloat: parsing "tcp://10.110.228.193:9090"`.
//
// The packages that name no runtimeConfigRef (the cloud providers, the
// families they depend on, every Function) get the runtime config called
// `default`, which Crossplane creates EMPTY — so it has to be declared, and
// a named one replaces it rather than adding to it, so each must repeat the
// field.
func TestPackageRuntimeConfigsDisableServiceLinks(t *testing.T) {
	configs := runtimeConfigs(t)
	if _, ok := configs["default"]; !ok {
		t.Fatalf("no DeploymentRuntimeConfig named default in the configuration tree (found %v); "+
			"the cloud providers and every Function run with Crossplane's empty one, "+
			"which keeps service links on", keysOf(configs))
	}
	for name, doc := range configs {
		spec := nestedMap(doc, "spec", "deploymentTemplate", "spec")
		if spec == nil {
			t.Errorf("DeploymentRuntimeConfig/%s has no deploymentTemplate.spec, so its package "+
				"runtimes keep service links on", name)
			continue
		}
		// The CRD requires a selector and a template with containers; an apply
		// without them is rejected outright, and the controller logs it as one
		// failed object among many.
		if _, ok := spec["selector"]; !ok {
			t.Errorf("DeploymentRuntimeConfig/%s: deploymentTemplate.spec has no selector "+
				"(required by the CRD; `selector: {}` lets Crossplane fill it)", name)
		}
		pod := nestedMap(spec, "template", "spec")
		if containers, _ := pod["containers"].([]any); len(containers) == 0 {
			t.Errorf("DeploymentRuntimeConfig/%s: deploymentTemplate.spec.template.spec has no containers "+
				"(required by the CRD; name the package-runtime container)", name)
		}
		links, present := pod["enableServiceLinks"]
		if !present || links != false {
			t.Errorf("DeploymentRuntimeConfig/%s: deploymentTemplate.spec.template.spec.enableServiceLinks "+
				"is %v, want false", name, links)
		}
	}
}

// runtimeConfigs returns every DeploymentRuntimeConfig the controller applies
// from platform/controlplane/configuration, keyed by name.
func runtimeConfigs(t *testing.T) map[string]map[string]any {
	t.Helper()
	root := filepath.Join("..", "..", "controlplane", "configuration")
	found := map[string]map[string]any{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Contains(raw, []byte("kind: DeploymentRuntimeConfig")) {
			return nil
		}
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		for {
			var doc map[string]any
			if err := dec.Decode(&doc); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				t.Fatalf("parsing %s: %v", path, err)
			}
			if doc == nil || doc["kind"] != "DeploymentRuntimeConfig" {
				continue
			}
			name, _ := nestedMap(doc, "metadata")["name"].(string)
			found[name] = doc
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func nestedMap(obj map[string]any, path ...string) map[string]any {
	cur := obj
	for _, key := range path {
		next, ok := cur[key].(map[string]any)
		if !ok {
			return nil
		}
		cur = next
	}
	return cur
}
