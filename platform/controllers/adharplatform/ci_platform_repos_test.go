package adharplatform

import (
	"regexp"
	"strings"
	"testing"

	"adhar-io/adhar/globals"
)

// A push to a platform repo is platform traffic, not an application; app-ci
// must not build it. The CEL interceptors name the excluded repos literally,
// so every repo the bootstrap creates or mirrors in the platform org has to
// be in that list. `adhar` stays in it although bootstrap no longer mirrors the
// platform's source (the AI reads the knowledge bundle instead, 2026-10-23):
// clusters bootstrapped before then still carry that pull mirror until an
// operator deletes it, and each upstream sync would otherwise start an app-ci
// build — which is what happened while it was missing (2026-10-15).
func TestAppCiSkipsEveryPlatformRepo(t *testing.T) {
	body := readStackFile(t, "application/adhar-supply-chain/manifests/70-app-ci.yaml")
	lists := regexp.MustCompile(`body\.repository\.name in \[([^\]]+)\]`).FindAllStringSubmatch(body, -1)
	if len(lists) < 2 {
		t.Fatalf("expected the push and the pull-request interceptors to each carry a repository exclusion list, found %d", len(lists))
	}
	for _, m := range lists {
		for _, repo := range []string{
			globals.GitOpsRepoPackages,
			globals.GitOpsRepoEnvironments,
			globals.GitOpsRepoTemplates,
			"adhar", // the retired source mirror; see above
			"adhar-ui", "adhar-kit",
		} {
			if !strings.Contains(m[1], "'"+repo+"'") {
				t.Errorf("app-ci interceptor list %s does not exclude platform repo %q; a push to it starts an application build", m[1], repo)
			}
		}
	}
}
