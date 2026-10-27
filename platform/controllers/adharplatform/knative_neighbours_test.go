package adharplatform

// Guards for the packages that run knative.dev/pkg binaries in adhar-system
// beside the Knative operator: Tekton and kpack. They share that framework's
// default names (config-logging, config-observability, the `webhook` component
// and Secret/webhook-certs), which is how one package's names became another's
// fault while Knative Serving lived in the same namespace (2026-10-26).

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Within one package an object must appear once. The namespace rewrite in
// Tekton's generator landed the resolvers' config-logging and
// config-observability on the pipelines' names, so the Application carried each
// twice (RepeatedResourceWarning) and whichever applied last won.
func TestKnativeFrameworkPackagesShipEachObjectOnce(t *testing.T) {
	for _, pkg := range []string{"application/tekton", "application/buildpack", knativePackageDir} {
		dir := filepath.Join(stackPackagesDir(t), filepath.FromSlash(pkg), "manifests")
		seen := map[string]string{}
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".yaml") {
				return err
			}
			for _, doc := range yamlDocs(t, path) {
				kind, _ := doc["kind"].(string)
				name, _ := metaName(doc).(string)
				if kind == "" || name == "" {
					continue
				}
				key := fmt.Sprintf("%v %s %s/%s", doc["apiVersion"], kind, knativeString(doc, "metadata", "namespace"), name)
				if first, dup := seen[key]; dup {
					t.Errorf("%s ships %s twice (%s and %s); Argo CD applies one copy over the other",
						pkg, key, first, rel(t, path))
				}
				seen[key] = rel(t, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(seen) == 0 {
			t.Errorf("%s: found no objects; the walker is broken", pkg)
		}
	}
}

// kpack's webhook binary hardcodes Secret/webhook-certs and runs under the
// knative.dev/pkg component name `webhook` (so its leases are `webhook.*`).
// Neither is configurable: the 2026-10-25 rename to kpack-webhook-certs (with a
// WEBHOOK_SECRET_NAME env var kpack never reads, and a Role scoped to the new
// name) left the binary forbidden from writing the Secret it actually uses, and
// kpack's admission webhooks ran with no certificate at all. So the manifest
// keeps upstream's names, and nothing else in adhar-system may use them:
// sigstore's policy-controller (cosign) hardcodes the same Secret, and Knative
// Serving's webhook the same Secret and leases — which is why Serving runs in
// knative-serving (TestKnativeComponentsNeverInstallIntoThePlatformNamespace).
func TestKpackWebhookKeepsTheNamesItsBinaryHardcodes(t *testing.T) {
	path := filepath.Join(stackPackagesDir(t), "application", "buildpack", "manifests", "install.yaml")
	var secret, roleGrant bool
	for _, doc := range yamlDocs(t, path) {
		switch doc["kind"] {
		case "Secret":
			if metaName(doc) == "webhook-certs" {
				secret = true
			}
		case "Role":
			rules, _ := doc["rules"].([]any)
			for _, raw := range rules {
				r, _ := raw.(map[string]any)
				verbs := fmt.Sprint(r["verbs"])
				names, _ := r["resourceNames"].([]any)
				for _, n := range names {
					if n == "webhook-certs" && strings.Contains(verbs, "update") {
						roleGrant = true
					}
				}
			}
		case "Deployment":
			for _, c := range containersOf(doc["spec"].(map[string]any)) {
				env, _ := c["env"].([]any)
				for _, raw := range env {
					if e, _ := raw.(map[string]any); e["name"] == "WEBHOOK_SECRET_NAME" {
						t.Errorf("Deployment/%v sets WEBHOOK_SECRET_NAME; kpack ignores it and keeps using webhook-certs", metaName(doc))
					}
				}
			}
		}
	}
	if !secret {
		t.Error("buildpack does not ship Secret/webhook-certs, the Secret kpack's webhook binary populates")
	}
	if !roleGrant {
		t.Error("no Role lets kpack's webhook update Secret/webhook-certs; its certificate reconciler is forbidden and admission runs without TLS")
	}

	for _, appset := range []string{"adhar-appset-local.yaml", "adhar-appset-production.yaml"} {
		enabled := map[string]bool{}
		for _, e := range loadAppSetElements(t, appset) {
			enabled[e.Name] = e.Enabled == "true"
		}
		if enabled["buildpack"] && enabled["cosign"] {
			t.Errorf("%s enables both buildpack and cosign; both webhooks hardcode Secret/webhook-certs in adhar-system, so one of them never gets a certificate", appset)
		}
	}

	// kpack's webhook Service selects `role: webhook` (upstream's selector); a
	// second workload with that label in adhar-system would receive kpack's
	// admission calls. Knative Serving's webhook pods carry it.
	forEachPackageWorkload(t, func(pkg, file, kind, name string, spec map[string]any) {
		if dig(spec, "template", "metadata", "labels", "role") != "webhook" {
			return
		}
		if pkg != "buildpack" || name != "kpack-webhook" {
			t.Errorf("%s: %s/%s labels its pods role=webhook, which kpack's webhook Service selects", pkg, kind, name)
		}
	})
}
