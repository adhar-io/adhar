package adharplatform

import (
	"os"
	"strings"
	"testing"
)

// The Gitea OAuth hook must never report success without an auth source.
//
// `gitea admin auth add-oauth` does OIDC discovery itself and fails outright
// when the issuer is not answering. The hook used to swallow that — a warning
// and a zero exit — on the reasoning that a transient hiccup should not wedge
// the keycloak app. It converts a transient hiccup into a permanent outage
// instead, because an Argo CD Sync hook does not run again once the app
// converges: there is nothing left to retry it.
//
// Live on 2026-10-13, while the gateway was returning 503 during a node
// outage:
//
//	Failed to create OpenID Connect Provider with name 'keycloak' …
//	  Non-success code for Discovery URL: 503
//	command terminated with exit code 1
//	WARN: gitea add-oauth failed (gitea local login still works)
//
// The Job reported Complete. `gitea admin auth list` was empty, and every
// Keycloak login into Gitea was broken until the command was run by hand.
func TestTheGiteaOAuthHookCannotSucceedWithoutConfiguringTheAuthSource(t *testing.T) {
	script := giteaOAuthHook(t)

	// It must wait for discovery rather than assume Keycloak is already up.
	if !strings.Contains(script, "well-known/openid-configuration") ||
		!strings.Contains(script, "OIDC_READY") {
		t.Error("the hook does not wait for Keycloak's discovery document before configuring " +
			"Gitea; add-oauth performs discovery itself and fails when the issuer is not answering")
	}

	// And every failure path must exit non-zero so Argo CD retries it.
	for _, verb := range []string{"add-oauth", "update-oauth"} {
		if !strings.Contains(script, verb) {
			t.Fatalf("the hook no longer runs %s; this guard needs updating", verb)
		}
	}
	if strings.Contains(script, "exit 1") == false {
		t.Error("no failure path in the hook exits non-zero, so the Job reports Complete " +
			"whatever happened and nothing ever retries it")
	}

	// The specific shape of the original defect: a failing oauth command whose
	// only consequence is a log line. Checked with comments stripped so the
	// prose above (and in the manifest) cannot satisfy or trip the check.
	for _, line := range strings.Split(stripShellComments(script), "\n") {
		if !strings.Contains(line, "oauth") {
			continue
		}
		if strings.Contains(line, "|| echo") {
			t.Errorf("an oauth command's failure is only logged, never surfaced: %q",
				strings.TrimSpace(line))
		}
	}
}

// stripShellComments drops whole-line `#` comments so a text guard reads the
// script rather than the prose explaining it.
func stripShellComments(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func giteaOAuthHook(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../stack/packages/security/keycloak/manifests/gitea-oauth-config.yaml")
	if err != nil {
		t.Fatalf("reading the gitea oauth manifest: %v", err)
	}
	return string(b)
}
