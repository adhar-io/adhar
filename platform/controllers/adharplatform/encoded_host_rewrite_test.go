package adharplatform

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"adhar-io/adhar/api/v1alpha1"
)

// The staging host rewrite must reach inside base64 Secret data.
//
// `stageStack` rewrites the raw text of every staged file, which covers
// ConfigMaps, HTTPRoutes and `stringData`. A Secret keeps its values
// base64-encoded under `data:`, where a text replace cannot see them, so any
// package shipping configuration that way carried the local hostname onto
// every cloud install.
//
// Live on 2026-10-13: the JupyterHub chart renders its whole values file into
// `Secret/hub` under `data: values.yaml`. Argo CD reported jupyterhub Synced
// AND Healthy, its HTTPRoute was correctly `jupyterhub.platform.adhar.io`, and
// `GET /hub/oauth_login` still answered:
//
//	302 -> https://keycloak.adhar.localtest.me:8443/realms/adhar/protocol/
//	       openid-connect/auth?...&redirect_uri=https%3A%2F%2Fjupyterhub.
//	       adhar.localtest.me%3A8443%2Fhub%2Foauth_callback
//
// — a host that resolves nowhere off a laptop, so SSO was simply broken.
func TestStagingRewritesHostnamesInsideBase64SecretData(t *testing.T) {
	src := t.TempDir()
	inner := "hub:\n  config:\n    GenericOAuthenticator:\n" +
		"      authorize_url: https://keycloak.adhar.localtest.me:8443/realms/adhar/protocol/openid-connect/auth\n" +
		"      oauth_callback_url: https://jupyterhub.adhar.localtest.me:8443/hub/oauth_callback\n"
	manifest := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: hub\ntype: Opaque\ndata:\n  values.yaml: \"" +
		base64.StdEncoding.EncodeToString([]byte(inner)) + "\"\n"
	mustWrite(t, filepath.Join(src, "pkg/manifests/install.yaml"), manifest)

	out, cleanup, err := StageStack(src, v1alpha1.BuildCustomizationSpec{
		Protocol: "https", Host: "platform.adhar.io", Port: "443",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	staged, err := os.ReadFile(filepath.Join(out, "pkg/manifests/install.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	decoded := decodeSecretValue(t, string(staged))
	if strings.Contains(decoded, "adhar.localtest.me") {
		t.Errorf("the staged Secret still carries the local hostname inside base64:\n%s", decoded)
	}
	for _, want := range []string{
		"https://keycloak.platform.adhar.io/realms/adhar/protocol/openid-connect/auth",
		"https://jupyterhub.platform.adhar.io/hub/oauth_callback",
	} {
		if !strings.Contains(decoded, want) {
			t.Errorf("staged Secret is missing %q; got:\n%s", want, decoded)
		}
	}
}

// Only blobs that actually carry the hostname may be DECODED AND RE-ENCODED.
//
// Asserting on the output bytes is not enough: rewriting a blob that never
// mentioned the host re-encodes it to exactly the same string, so a guard that
// only compares bytes passes whether or not the check exists (it did, and was
// rewritten for that reason). What the check buys is that a TLS key, a token
// or a binary is never round-tripped through decode/encode at all — on a 62 MB
// tree that is both the cost and the blast radius. So watch the rewrite
// function instead of the bytes.
func TestStagingNeverRoundTripsBase64ThatLacksTheHost(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte(
		"-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcw\n-----END PRIVATE KEY-----\n"))
	withHost := base64.StdEncoding.EncodeToString([]byte("url: https://x.adhar.localtest.me:8443/a"))
	doc := "data:\n  tls.key: \"" + key + "\"\n  conf: \"" + withHost + "\"\n"

	var seen []string
	out := rewriteEncodedHosts(doc, func(in string) string {
		seen = append(seen, in)
		return strings.ReplaceAll(in, "adhar.localtest.me:8443", "platform.adhar.io")
	})

	if len(seen) != 1 {
		t.Fatalf("the rewrite ran on %d blob(s), want exactly the one carrying the host: %q", len(seen), seen)
	}
	if strings.Contains(seen[0], "PRIVATE KEY") {
		t.Error("the private key was decoded and re-encoded; only blobs mentioning the host may be touched")
	}
	if !strings.Contains(out, key) {
		t.Error("the private key blob was modified")
	}
	if strings.Contains(out, withHost) {
		t.Error("the blob carrying the host was left unrewritten")
	}
}

func decodeSecretValue(t *testing.T, manifest string) string {
	t.Helper()
	i := strings.Index(manifest, "values.yaml: ")
	if i < 0 {
		t.Fatalf("no values.yaml key in staged manifest:\n%s", manifest)
	}
	blob := strings.TrimSpace(manifest[i+len("values.yaml: "):])
	blob = strings.Trim(blob, "\"\n")
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		t.Fatalf("staged value is no longer valid base64: %v", err)
	}
	return string(raw)
}
