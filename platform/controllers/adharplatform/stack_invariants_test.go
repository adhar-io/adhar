package adharplatform

// Regression guards for platform-wide invariants that a single package can
// silently break and that only show up on a live cluster. Each one is a bug
// found on the 2026-09-15 DigitalOcean bring-up; the checks read the stack
// tree exactly as it is seeded into Gitea, so they run in `make test` without
// a cluster.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func stackPackageManifests(t *testing.T) []string {
	t.Helper()
	root := filepath.Join(stackRoot(t), "packages")
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yaml.tmpl") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	if len(files) < 100 {
		t.Fatalf("expected the whole package tree, found %d manifests under %s", len(files), root)
	}
	return files
}

func stackRoot(t *testing.T) string {
	t.Helper()
	// platform/controllers/adharplatform → platform/stack
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", "..", "stack"))
}

// yamlDocs splits a manifest into documents, tolerating template files: a
// document that does not parse (Go template syntax) is skipped, since the
// invariants below are about plain Kubernetes objects.
func yamlDocs(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var docs []map[string]any
	for _, chunk := range strings.Split(string(raw), "\n---") {
		var doc map[string]any
		if err := yaml.Unmarshal([]byte(chunk), &doc); err != nil || doc == nil {
			continue
		}
		docs = append(docs, doc)
	}
	return docs
}

func metaAnnotations(doc map[string]any) map[string]any {
	meta, _ := doc["metadata"].(map[string]any)
	ann, _ := meta["annotations"].(map[string]any)
	return ann
}

func metaLabels(doc map[string]any) map[string]any {
	meta, _ := doc["metadata"].(map[string]any)
	labels, _ := meta["labels"].(map[string]any)
	return labels
}

// Every package installs into adhar-system (ADR-0011), so a Namespace object
// vendored from an upstream release that carries pod-security
// `enforce: restricted` is the PLATFORM namespace carrying it — every sync of
// that package then stamps the label on until another Application removes it,
// and in between plain pods (a Tekton clone step, a `kubectl run`) are refused
// with `violates PodSecurity "restricted:latest"`. Tekton shipped exactly that
// and it took a day to trace; kpack and Kubeflow generators already drop theirs.
func TestNoPackageShipsAPodSecurityEnforcingNamespace(t *testing.T) {
	for _, f := range stackPackageManifests(t) {
		for _, doc := range yamlDocs(t, f) {
			if doc["kind"] != "Namespace" {
				continue
			}
			for k := range metaLabels(doc) {
				if strings.HasPrefix(k, "pod-security.kubernetes.io/") {
					t.Errorf("%s: Namespace %v carries %s — drop the Namespace document in the package generator (packages/CONFLICTS.md)", rel(t, f), metaName(doc), k)
				}
			}
		}
	}
}

// A Job's selector is server-generated and immutable, so an Argo CD
// `Replace=true` sync of a Job that already exists is a plain PUT that the API
// server rejects with "spec.selector: Required value" on EVERY re-sync — the
// whole Application wedges. `Force=true` (delete + recreate) is what makes
// Replace work for Jobs. rustfs-buckets, adhar-ai-bot-provision and posthog's
// clickhouse-prepare all had this.
func TestJobsSyncedWithReplaceAlsoForce(t *testing.T) {
	for _, f := range stackPackageManifests(t) {
		for _, doc := range yamlDocs(t, f) {
			if doc["kind"] != "Job" {
				continue
			}
			opts, _ := metaAnnotations(doc)["argocd.argoproj.io/sync-options"].(string)
			if strings.Contains(opts, "Replace=true") && !strings.Contains(opts, "Force=true") {
				t.Errorf("%s: Job %v has sync-options %q — Replace on a Job needs Force=true as well", rel(t, f), metaName(doc), opts)
			}
		}
	}
}

// The Kyverno chart excludes its own release namespace from both the resource
// filters and the webhook namespaceSelector; on this platform that namespace
// is adhar-system, i.e. every package. With the default in place no mutate
// rule aimed at platform pods (the kpack build-pod CA injection) ever fires.
func TestKyvernoAdmitsThePlatformNamespace(t *testing.T) {
	values := filepath.Join(stackRoot(t), "packages", "security", "kyverno", "values.yaml")
	raw, err := os.ReadFile(values)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Config struct {
			ExcludeKyvernoNamespace *bool `yaml:"excludeKyvernoNamespace"`
		} `yaml:"config"`
	}
	if err := yaml.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if v.Config.ExcludeKyvernoNamespace == nil || *v.Config.ExcludeKyvernoNamespace {
		t.Fatalf("%s must set config.excludeKyvernoNamespace: false — otherwise Kyverno never sees adhar-system", values)
	}
	rendered, err := os.ReadFile(filepath.Join(stackRoot(t), "packages", "security", "kyverno", "manifests", "install.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rendered), "adhar-system,*") {
		t.Fatalf("the rendered kyverno install.yaml still lists adhar-system in resourceFilters — regenerate it")
	}
}

func metaName(doc map[string]any) any {
	meta, _ := doc["metadata"].(map[string]any)
	return meta["name"]
}

func rel(t *testing.T, path string) string {
	t.Helper()
	if r, err := filepath.Rel(stackRoot(t), path); err == nil {
		return r
	}
	return path
}

// Gitea's default webhook.ALLOWED_HOST_LIST is `external`, which refuses the
// cluster-internal Tekton EventListeners every platform CI hook points at —
// nothing scaffolded is ever built. And `passwordMode: keepUpdated` resets the
// rotated admin password on every pod restart, locking out everything that
// reads Secret/gitea-credential. Both are bootstrap values, so guard them here.
func TestGiteaBootstrapValuesKeepCIAndRotationWorking(t *testing.T) {
	for _, name := range []string{"values.yaml", "values-ha.yaml"} {
		path := filepath.Join(stackRoot(t), "..", "..", "hack", "gitea", name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		s := string(raw)
		if !strings.Contains(s, "ALLOWED_HOST_LIST: external,private,*.svc.cluster.local") {
			t.Errorf("%s: webhook.ALLOWED_HOST_LIST must allow in-cluster targets (el-app-ci / el-adhar-ci)", name)
		}
		if strings.Contains(s, "passwordMode: keepUpdated") {
			t.Errorf("%s: gitea.admin.passwordMode keepUpdated undoes the platform's password rotation on restart", name)
		}
	}
	for _, name := range []string{"install.yaml", "install-ha.yaml"} {
		raw, err := os.ReadFile(filepath.Join("resources", "gitea", name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "ALLOWED_HOST_LIST=external,private,*.svc.cluster.local") {
			t.Errorf("resources/gitea/%s: embedded manifest lacks the webhook allow-list — regenerate it", name)
		}
	}
}

// Every enabled ApplicationSet element must point at a directory that exists,
// and — because Kyverno is off in the local curated core — no enabled local
// element may ship kyverno.io resources unless the kyverno engine itself is
// enabled there. A violation means an Application that can never sync.
func TestEnabledElementsExistAndKyvernoResourcesFollowTheEngine(t *testing.T) {
	root := stackRoot(t)
	for _, appset := range []string{"adhar-appset-local.yaml", "adhar-appset-production.yaml"} {
		raw, err := os.ReadFile(filepath.Join(root, appset))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Spec struct {
				Generators []struct {
					List struct {
						Elements []map[string]string `yaml:"elements"`
					} `yaml:"list"`
				} `yaml:"generators"`
			} `yaml:"spec"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", appset, err)
		}
		kyvernoOn := false
		var enabled []map[string]string
		for _, g := range doc.Spec.Generators {
			for _, e := range g.List.Elements {
				if e["enabled"] != "true" {
					continue
				}
				enabled = append(enabled, e)
				if e["name"] == "kyverno" {
					kyvernoOn = true
				}
			}
		}
		for _, e := range enabled {
			dir := filepath.Join(root, "packages", e["manifestPath"])
			if st, err := os.Stat(dir); err != nil || !st.IsDir() {
				t.Errorf("%s: element %s points at missing directory %s", appset, e["name"], e["manifestPath"])
				continue
			}
			if kyvernoOn {
				continue
			}
			entries, _ := os.ReadDir(dir)
			for _, f := range entries {
				if f.IsDir() || !strings.HasSuffix(f.Name(), ".yaml") {
					continue
				}
				b, _ := os.ReadFile(filepath.Join(dir, f.Name()))
				if strings.Contains(string(b), "kyverno.io/v") {
					t.Errorf("%s: element %s ships kyverno.io resources (%s) while the kyverno engine is disabled — it can never sync", appset, e["name"], f.Name())
				}
			}
		}
	}
}

// No package may ship a probe with a one-second timeout.
//
// Measured on a loaded local cluster: harbor-database's liveness and readiness
// probes (timeoutSeconds: 1, the upstream chart default) failed 377 times in
// 22 hours — "command timed out: /docker-healthcheck.sh timed out after 1s" —
// on a database that was up and logging "ready to accept connections". Kubelet
// killed it 29 times; harbor-core then FATALs after its own 60 s database wait,
// which took jobservice, registry, nginx and exporter down with it (55-66
// restarts each). A probe timeout must leave room for a contended node.
func TestNoProbeShipsAOneSecondTimeout(t *testing.T) {
	root := filepath.Join(stackRoot(t), "packages")
	probe := regexp.MustCompile(`\b(liveness|readiness|startup)Probe:`)
	tight := regexp.MustCompile(`^\s*timeoutSeconds: 1$`)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !(strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")) {
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		lines := strings.Split(string(b), "\n")
		last := -99
		for i, l := range lines {
			if probe.MatchString(l) {
				last = i
			}
			if tight.MatchString(l) && i-last <= 14 {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s:%d ships a probe with timeoutSeconds: 1 — it will kill healthy pods on a loaded node", rel, i+1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A package must not own an ExternalSecret whose source Secret is created by a
// DIFFERENT, independently-enabled package: on a profile where that package is
// off, the ExternalSecret can never resolve, the owning app fails its sync on
// "could not get secret data from provider", and ArgoCD retries forever (the
// console hit attempt #39 on the local profile before coder-credentials moved
// into the coder package, 2026-09-19).
func TestPackagesDoNotOwnExternalSecretsSourcedFromOtherPackages(t *testing.T) {
	root := filepath.Join(stackRoot(t), "packages")
	// source Secret name -> package directory that creates it
	crossPackage := map[string]string{
		"coder-admin-credentials": "application/coder",
		"plane-api-credentials":   "application/plane",
	}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		body := string(b)
		if !strings.Contains(body, "kind: ExternalSecret") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		owner := filepath.ToSlash(filepath.Dir(filepath.Dir(rel))) // <category>/<package>
		for source, producer := range crossPackage {
			if strings.Contains(body, "key: "+source) && owner != producer {
				t.Errorf("%s: package %s owns an ExternalSecret reading %q, which only %s creates — move it into %s",
					rel, owner, source, producer, producer)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
