package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// schemalessCRDKinds returns "group/Kind" for every CRD the stack installs
// whose storage version publishes a root with no properties — the shape
// Tekton's CRDs ship (`type: object`, `x-kubernetes-preserve-unknown-fields:
// true`). Argo CD v3 builds its RespectIgnoreDifferences patch from the
// cluster's OpenAPI schema, and that schema has no fields to descend into.
func schemalessCRDKinds(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	root := filepath.Join(stackRoot(t), "packages")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return err
		}
		for _, doc := range yamlDocs(t, path) {
			if doc["kind"] != "CustomResourceDefinition" {
				continue
			}
			spec, _ := doc["spec"].(map[string]any)
			names, _ := spec["names"].(map[string]any)
			group, _ := spec["group"].(string)
			kind, _ := names["kind"].(string)
			versions, _ := spec["versions"].([]any)
			for _, v := range versions {
				ver, _ := v.(map[string]any)
				if ver["storage"] != true {
					continue
				}
				schema, _ := ver["schema"].(map[string]any)
				openAPI, _ := schema["openAPIV3Schema"].(map[string]any)
				props, _ := openAPI["properties"].(map[string]any)
				specProps := map[string]any{}
				if s, ok := props["spec"].(map[string]any); ok {
					specProps, _ = s["properties"].(map[string]any)
				}
				if len(props) == 0 || len(specProps) == 0 {
					out[group+"/"+kind] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// No ApplicationSet may carry an ignoreDifferences rule for a kind whose CRD
// is schemaless (2026-11-22). With RespectIgnoreDifferences=true, Argo CD v3
// copies each ignored live field into the target through a strategic merge
// built from the CRD's OpenAPI schema; a root with no properties fails that
// lookup ("Failed to normalize target resources: expected kind, but got map").
// The first sync passes, because nothing is live yet; every retry after the
// objects exist fails forever. On a local `adhar up` that wedged
// adhar-supply-chain and adhar-libraries, so their PostSync hooks never
// registered the Gitea CI webhooks and `Project adhar` was never applied: no
// new app could get environments and no push could build.
func TestNoIgnoreDifferencesReachIntoASchemalessCRD(t *testing.T) {
	schemaless := schemalessCRDKinds(t)
	for _, want := range []string{"tekton.dev/Task", "tekton.dev/Pipeline", "triggers.tekton.dev/EventListener"} {
		if !schemaless[want] {
			t.Fatalf("expected %s to be detected as schemaless; the CRD scan is broken (found %d)", want, len(schemaless))
		}
	}
	appsets, err := filepath.Glob(filepath.Join(stackRoot(t), "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, path := range appsets {
		for _, doc := range yamlDocs(t, path) {
			if doc["kind"] != "ApplicationSet" {
				continue
			}
			spec, _ := doc["spec"].(map[string]any)
			tmpl, _ := spec["template"].(map[string]any)
			tspec, _ := tmpl["spec"].(map[string]any)
			rules, _ := tspec["ignoreDifferences"].([]any)
			checked++
			for _, r := range rules {
				rule, _ := r.(map[string]any)
				group, _ := rule["group"].(string)
				kind, _ := rule["kind"].(string)
				if schemaless[group+"/"+kind] {
					t.Errorf("%s: ignoreDifferences rule for %s/%s reaches into a schemaless CRD; "+
						"with RespectIgnoreDifferences every sync retry fails to normalize. Remove it — "+
						"server-side diff already absorbs admission defaults",
						filepath.Base(path), group, kind)
				}
			}
		}
	}
	if checked < 2 {
		t.Fatalf("checked %d ApplicationSets; expected at least the local and production ones", checked)
	}
}

// The CA distributor must restart the kpack controller in the namespace the
// buildpack package installs it in. kpack loads its trust store once; its
// ClusterStore pulls over TLS before the distributor has written adhar-ca, so
// without a restart the ClusterBuilder fails "x509: certificate signed by
// unknown authority" forever. The restart named kpack-system for two months
// after kpack moved to adhar-system, so it never happened (2026-11-22).
func TestTheCADistributorRestartsKpackWhereItRuns(t *testing.T) {
	var kpackNS string
	for _, doc := range yamlDocs(t, filepath.Join(stackPackagesDir(t), "application", "buildpack", "manifests", "install.yaml")) {
		meta, _ := doc["metadata"].(map[string]any)
		if doc["kind"] == "Deployment" && meta["name"] == "kpack-controller" {
			kpackNS, _ = meta["namespace"].(string)
		}
	}
	if kpackNS == "" {
		t.Fatal("no kpack-controller Deployment with a namespace in the buildpack package")
	}
	job := readStackFile(t, "application/adhar-supply-chain/manifests/05-ca-trust.yaml")
	loop := regexp.MustCompile(`for d in ([^;]+); do`).FindStringSubmatch(job)
	if loop == nil || !strings.Contains(loop[1], "kpack-controller") {
		t.Fatal("05-ca-trust.yaml no longer rolls kpack-controller; kpack would keep a trust store without the Harbor CA")
	}
	rolls := regexp.MustCompile(`kubectl -n (\S+) (?:patch|rollout restart) deployment/`).FindAllStringSubmatch(job, -1)
	if len(rolls) == 0 {
		t.Fatal("05-ca-trust.yaml rolls no Deployment")
	}
	for _, m := range rolls {
		if m[1] != kpackNS {
			t.Errorf("05-ca-trust.yaml rolls Deployments in %q, but the buildpack package installs kpack-controller in %q", m[1], kpackNS)
		}
	}
	if strings.Contains(job, "-n kpack-system") {
		t.Errorf("05-ca-trust.yaml still writes to or restarts in kpack-system, a namespace the platform no longer creates")
	}
}

// The CA distributor is a Sync hook, so it runs on every adhar-supply-chain
// sync. It must roll a controller only when the CA bundle that controller runs
// with is not the current one; an unconditional restart bounces kpack and
// Kyverno on every push to the package, on every cloud cluster (2026-11-22).
func TestTheCADistributorRestartsOnlyWhenTheBundleChanged(t *testing.T) {
	job := readStackFile(t, "application/adhar-supply-chain/manifests/05-ca-trust.yaml")
	var code []string
	for _, line := range strings.Split(job, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			code = append(code, line)
		}
	}
	script := strings.Join(code, "\n")
	if strings.Contains(script, "rollout restart") {
		t.Error("05-ca-trust.yaml restarts a controller unconditionally on every sync")
	}
	for _, want := range []string{`sha256sum /tmp/ca.crt`, `adhar\.io/ca-bundle`, `[ "$have" = "$bundle" ]`} {
		if !strings.Contains(script, want) {
			t.Errorf("05-ca-trust.yaml must compare the running bundle hash before rolling; missing %q", want)
		}
	}
}
