package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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
