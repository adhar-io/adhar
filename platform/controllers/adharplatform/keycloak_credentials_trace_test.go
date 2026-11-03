package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The keycloak package's jobs hold the platform's identity credentials in
// shell variables. configure.sh (the realm Sync hook AND the client reconcile
// CronJob that runs it every five minutes) reads the master-realm admin
// password, the realm users' passwords, an Argo CD admin session and every
// OIDC client secret; gitea-oauth-config reads the Gitea client secret. Under
// `set -x` bash prints each command and assignment with its values expanded,
// so every one of them reached the pod log, which every platform-viewer may
// read (pods/log in adhar-system) and which Loki keeps. Live on 2026-11-03 a
// single reconcile run printed the master admin password, two master-realm
// admin tokens, the Argo CD admin password and 41 client secrets.
//
// No script in that package, and no script anywhere in the stack that reads
// the master-realm admin password, may turn tracing on. A
// `{ set +x; ...; set -x; }` wrapper counts too: it switches tracing back on
// for everything after it.
func TestKeycloakJobsNeverTraceTheirCredentials(t *testing.T) {
	root := stackPackagesDir(t)
	keycloakDir := filepath.Join(root, "security", "keycloak", "manifests")
	xtrace := regexp.MustCompile(`\bset\s+-[A-Za-z]*x|\bset\s+-o\s+xtrace\b|\b(?:ba)?sh\s+-[A-Za-z]*x|BASH_XTRACEFD`)
	checked := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !(strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yaml.tmpl")) {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(b)
		if filepath.Dir(path) != keycloakDir && !strings.Contains(src, "KEYCLOAK_ADMIN_PASSWORD") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		checked[filepath.ToSlash(rel)] = true
		for i, line := range strings.Split(src, "\n") {
			// A script inlined as one JSON-escaped string (gitea-oauth-config)
			// carries its own lines as `\n`: each is checked on its own, and
			// its comments are skipped like the YAML's.
			for _, part := range strings.Split(line, `\n`) {
				if strings.HasPrefix(strings.TrimSpace(part), "#") {
					continue
				}
				if m := xtrace.FindString(part); m != "" {
					t.Errorf("%s:%d turns shell tracing on (%q): every credential the script reads would be printed into the pod log", rel, i+1, m)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"security/keycloak/manifests/keycloak-config.yaml",
		"security/keycloak/manifests/gitea-oauth-config.yaml",
		"data/jupyterhub/manifests/jupyterhub-config.yaml",
	} {
		if !checked[rel] {
			t.Errorf("%s is no longer checked; re-check which jobs hold the platform's identity credentials", rel)
		}
	}
}
