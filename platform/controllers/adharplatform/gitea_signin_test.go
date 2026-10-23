package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"adhar-io/adhar/globals"
)

// Anonymous visitors could list and read every repository on the platform's
// Gitea (2026-10-21). REQUIRE_SIGNIN_VIEW closes that — and breaks every reader
// that never sent a credential, so the setting and the readers' credentials
// are pinned together here.
func TestGiteaRequiresSignInAndEveryReaderAuthenticates(t *testing.T) {
	read := func(p ...string) string {
		b, err := os.ReadFile(filepath.Join(p...))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	tmpl := regexp.MustCompile(`\{\{[^}]*\}\}`)

	// 1. The setting, in both renderings, with the config secret intact: the
	//    session block once had the webhook line inside it, leaving [session]
	//    empty and the session settings glued onto ALLOWED_HOST_LIST.
	for _, f := range []string{"install.yaml", "install-ha.yaml"} {
		txt := tmpl.ReplaceAllString(read("resources", "gitea", f), "X")
		found := false
		for _, doc := range strings.Split(txt, "\n---\n") {
			var s struct {
				Kind       string            `yaml:"kind"`
				StringData map[string]string `yaml:"stringData"`
			}
			if yaml.Unmarshal([]byte(doc), &s) != nil || s.Kind != "Secret" || s.StringData["session"] == "" && s.StringData["webhook"] == "" {
				continue
			}
			found = true
			if s.StringData["service"] != "REQUIRE_SIGNIN_VIEW=true" {
				t.Errorf("%s: [service] is %q; anonymous visitors can read every repository", f, s.StringData["service"])
			}
			if !strings.HasPrefix(s.StringData["session"], "PROVIDER=redis") {
				t.Errorf("%s: [session] is %q", f, s.StringData["session"])
			}
			if strings.Contains(s.StringData["webhook"], "PROVIDER") {
				t.Errorf("%s: session settings leaked into [webhook]: %q", f, s.StringData["webhook"])
			}
		}
		if !found {
			t.Fatalf("%s: no Gitea config Secret found", f)
		}
	}
	for _, f := range []string{"values.yaml", "values-ha.yaml"} {
		if !strings.Contains(read("..", "..", "..", "hack", "gitea", f), "    service:\n      REQUIRE_SIGNIN_VIEW: true\n") {
			t.Errorf("hack/gitea/%s lacks REQUIRE_SIGNIN_VIEW; regenerating would reopen Gitea", f)
		}
	}

	// 2. Argo CD: a PREFIX credential for every in-cluster Gitea host.
	auth := read("..", "..", "stack", "argocd-auth.yaml")
	for _, prefix := range []string{
		"http://gitea-http.adhar-system.svc.cluster.local:3000/",
		"http://gitea-argocd.adhar-system.svc.cluster.local:3000/",
	} {
		ok := false
		for _, doc := range strings.Split(auth, "\n---\n") {
			if strings.Contains(doc, "argocd.argoproj.io/secret-type: repo-creds") && strings.Contains(doc, "url: "+prefix+"\n") {
				ok = true
			}
		}
		if !ok {
			t.Errorf("no repo-creds template for %s: Argo CD clones those repositories anonymously and fails", prefix)
		}
	}

	pkgs := filepath.Join("..", "..", "stack", "packages")
	// 3. CI clones with the CI credential, scoped to the Gitea hosts.
	clone := read(pkgs, "application", "adhar-supply-chain", "manifests", "60-build-pipelines.yaml")
	if !strings.Contains(clone, "secretKeyRef: {name: gitea-ci-creds, key: password") ||
		!strings.Contains(clone, `git -c http.extraHeader="$AUTH" clone`) ||
		!strings.Contains(clone, "http://gitea-http.adhar-system.svc.cluster.local:3000/*") {
		t.Error("the git-clone Task no longer authenticates to the in-cluster Gitea")
	}
	// 4. The AI knowledge base reads with the bot token.
	for _, f := range []string{"agent-runtime.yaml", "knowledge-reindex.yaml"} {
		if !strings.Contains(read(pkgs, "ai", "adhar-ai", "manifests", f), "name: GITEA_BOT_TOKEN") {
			t.Errorf("%s reads Gitea without a token", f)
		}
	}
	// 5. Readiness probes use the one exempt route.
	if strings.Contains(read(pkgs, "ai", "adhar-ai", "manifests", "bot-provision.yaml"), `"${GITEA}/api/v1/version" || echo 000`) {
		t.Error("bot-provision probes /api/v1/version anonymously; it gets 401 forever")
	}
	if strings.Contains(read("controller.go"), `"curl", "-sf", "http://localhost:3000/api/v1/version"`) {
		t.Error("the controller's Gitea readiness wait probes /api/v1/version anonymously; bootstrap would fail")
	}
}

// Argo CD reads Gitea as gitea_admin, with the password the CLI bootstrap wrote
// into argocd-auth.yaml's Secrets. The day-0 credential rotation then changes
// that password minutes later, and the in-cluster manager never re-applies
// those Secrets (it has no stack directory). Once Gitea required sign-in, every
// Application went Unknown with "authentication required" and a fresh
// DigitalOcean `adhar up` failed waiting for the console (2026-10-22). So the
// rotation must update EVERY Secret here that carries the password — and no
// ConfigMap may carry it, because the controller substitutes the rotated one.
func TestCredentialRotationKeepsArgoCDsGiteaCredentialsCurrent(t *testing.T) {
	auth, err := os.ReadFile(filepath.Join("..", "..", "stack", "argocd-auth.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	job, err := os.ReadFile(filepath.Join("..", "..", "stack", "packages", "security",
		"adhar-credential-rotation", "manifests", "rotate-job.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	loop := regexp.MustCompile(`(?m)^\s*for s in ([^;]+); do\n(?:.*\n){0,2}?.*kpatch "\$s" "\{\\"stringData\\": \{\\"password\\": \\"\$\{GITEA_NEW\}\\"`).
		FindStringSubmatch(string(job))
	if loop == nil {
		t.Fatal("rotate-job.yaml no longer patches Argo CD's Gitea credentials with GITEA_NEW")
	}
	patched := map[string]bool{}
	for _, n := range strings.Fields(loop[1]) {
		patched[n] = true
	}

	carrying := 0
	for _, doc := range strings.Split(string(auth), "\n---\n") {
		var o struct {
			Kind       string                `yaml:"kind"`
			Metadata   struct{ Name string } `yaml:"metadata"`
			Data       map[string]string     `yaml:"data"`
			StringData map[string]string     `yaml:"stringData"`
		}
		if yaml.Unmarshal([]byte(doc), &o) != nil {
			continue
		}
		switch o.Kind {
		case "ConfigMap":
			for k, v := range o.Data {
				if strings.Contains(strings.ToLower(k), "password") || strings.Contains(v, globals.GiteaAdminPassword) {
					t.Errorf("ConfigMap %s carries a Gitea password in %q", o.Metadata.Name, k)
				}
			}
		case "Secret":
			if o.StringData["password"] == "" {
				continue
			}
			carrying++
			if !patched[o.Metadata.Name] {
				t.Errorf("Secret %s carries the Gitea admin password but the rotation job does not update it", o.Metadata.Name)
			}
		}
	}
	if carrying < 4 {
		t.Fatalf("expected at least 4 Argo CD Gitea credentials in argocd-auth.yaml, found %d", carrying)
	}
}

// The console, CI and Kargo read the Gitea password through ExternalSecret
// copies of gitea-credential that refresh hourly, and the console takes it as
// env at start-up. A rotation that only patched the source left the Service
// Catalog showing "Gitea — Not authorised to read this source" for the next
// hour (DigitalOcean, 2026-10-23). The Job must resync every copy, restart the
// console, and hold the RBAC to do both.
func TestCredentialRotationPropagatesToEveryGiteaCredentialCopy(t *testing.T) {
	job, err := os.ReadFile(filepath.Join("..", "..", "stack", "packages", "security",
		"adhar-credential-rotation", "manifests", "rotate-job.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(job)
	for what, re := range map[string]string{
		"find every ExternalSecret copying gitea-credential": `index\("gitea-credential"\)`,
		"force-sync those ExternalSecrets":                   `\\"force-sync\\"`,
		"wait for the console's copy to carry GITEA_NEW":     `\.data\.GITEA_PASSWORD[^\n]*\n?[^\n]*= "\$\{GITEA_NEW\}"`,
		"restart the console":                                `restartedAt[\s\S]*deployments/adhar-console`,
		"RBAC to list and patch ExternalSecrets":             `resources: \["externalsecrets"\]\s*\n\s*verbs: \["list", "patch"\]`,
		"RBAC to patch the console Deployment":               `resourceNames: \["adhar-console"\]\s*\n\s*verbs: \["get", "patch"\]`,
		"bind the ClusterRole to the Job's ServiceAccount":   `kind: ClusterRoleBinding[\s\S]*?name: credential-rotation\n\s*namespace: adhar-system`,
	} {
		if !regexp.MustCompile(re).MatchString(src) {
			t.Errorf("rotate-job.yaml must %s", what)
		}
	}
}
