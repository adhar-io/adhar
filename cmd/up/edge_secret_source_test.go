package up

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"adhar-io/adhar/platform/config"
)

// The in-cluster DNS Secret must carry the SAME credential `adhar up` used.
//
// edgeDNSSecretData resolved the Azure secret with a local rule,
// `get(pcv.ClientSecret, "AZURE_CLIENT_SECRET")`, which knew nothing about
// clientSecretFile. So the cluster was built from the secret FILE while this
// wrote a stale environment value into `adhar-dns-provider`: every node came up,
// every component synced, and then external-dns crash-looped on
//
//	level=fatal msg="Failed to do run once: ClientSecretCredential authentication
//	failed. RESPONSE 401 … AADSTS7000215"
//
// Nothing else failed. The only visible symptom was that every platform URL
// resolved to the PREVIOUS cluster's dead load-balancer IP, because external-dns
// never got far enough to update a record (2026-10-04).
func TestEdgeDNSSecretUsesTheConfiguredSecretFile(t *testing.T) {
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "azure-client-secret")
	const want = "the-secret-adhar-up-actually-used"
	if err := os.WriteFile(secretFile, []byte(want+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The trap: a stale export that must NOT win over the configured file.
	t.Setenv("AZURE_CLIENT_SECRET", "a-stale-secret-from-another-app")

	data, err := edgeDNSSecretData(context.Background(), dnsAzure, &config.ConfigProviderConfig{
		ClientID:         "client",
		ClientSecretFile: secretFile,
		TenantID:         "tenant",
		Config: map[string]interface{}{
			"subscriptionId":   "sub",
			"dnsResourceGroup": "adhar-rg",
		},
	})
	if err != nil {
		t.Fatalf("edgeDNSSecretData: %v", err)
	}

	// cert-manager's azureDNS solver reads this key.
	if got := string(data["client-secret"]); got != want {
		t.Errorf("client-secret came from the wrong source: got %q, want %q", got, want)
	}
	// external-dns reads this one, and it embeds the secret separately — so it
	// can be stale even when client-secret is right.
	if got := string(data["azure.json"]); !strings.Contains(got, want) {
		t.Errorf("azure.json does not carry the configured secret: %s", got)
	}
	if strings.Contains(string(data["azure.json"]), "a-stale-secret-from-another-app") {
		t.Error("azure.json carries the stale environment secret")
	}
}

// With no file configured the environment still works, so existing setups are
// unaffected.
func TestEdgeDNSSecretStillAcceptsTheEnvironment(t *testing.T) {
	t.Setenv("AZURE_CLIENT_SECRET", "from-env")
	data, err := edgeDNSSecretData(context.Background(), dnsAzure, &config.ConfigProviderConfig{
		ClientID: "client",
		TenantID: "tenant",
		Config: map[string]interface{}{
			"subscriptionId":   "sub",
			"dnsResourceGroup": "adhar-rg",
		},
	})
	if err != nil {
		t.Fatalf("edgeDNSSecretData: %v", err)
	}
	if got := string(data["client-secret"]); got != "from-env" {
		t.Errorf("got %q, want %q", got, "from-env")
	}
}

// A configured path that cannot be read must fail the step loudly rather than
// write an empty credential into the cluster, which only surfaces much later as
// a crash-looping external-dns.
func TestEdgeDNSSecretFailsOnAnUnreadableSecretFile(t *testing.T) {
	_, err := edgeDNSSecretData(context.Background(), dnsAzure, &config.ConfigProviderConfig{
		ClientID:         "client",
		ClientSecretFile: filepath.Join(t.TempDir(), "missing"),
		TenantID:         "tenant",
		Config: map[string]interface{}{
			"subscriptionId":   "sub",
			"dnsResourceGroup": "adhar-rg",
		},
	})
	if err == nil {
		t.Fatal("an unreadable secret file must fail the edge DNS step")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("the error must name the path, got: %v", err)
	}
}

// No credential may be resolved with a local rule.
//
// The bug was not a wrong value; it was a SECOND place that resolved the same
// credential with its own rules. Any new source added to config.ResolveSecret
// has to reach every consumer, which only holds if consumers call it rather than
// reading the environment themselves.
func TestEdgeDNSDoesNotReadTheAzureSecretDirectly(t *testing.T) {
	b, err := os.ReadFile("edge.go")
	if err != nil {
		t.Fatal(err)
	}
	// Code only: the explanation above the call names the old expression.
	var code strings.Builder
	for _, line := range strings.Split(string(b), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		code.WriteString(line + "\n")
	}
	src := code.String()
	if !strings.Contains(src, "config.ResolveSecret(") {
		t.Error("the Azure DNS credential must come from config.ResolveSecret, so that every " +
			"source it supports reaches the in-cluster Secret too")
	}
	if strings.Contains(src, `get(pcv.ClientSecret, "AZURE_CLIENT_SECRET")`) {
		t.Error("the Azure client secret is being resolved with a local rule again — that rule " +
			"cannot see clientSecretFile, which is how a stale environment value reached the " +
			"in-cluster Secret while the cluster itself used the file")
	}
}

// The Crossplane/controller credentials Secret must use the shared resolver too.
//
// `azure-credentials` is what the IN-CLUSTER controllers authenticate with — the
// node autoscaler among them. While crossplane_creds.go read only
// AZURE_CLIENT_SECRET it could not see clientSecretFile, so `adhar up` built the
// cluster from the file and wrote a STALE environment value here. The autoscaler
// then decided correctly that another worker was needed and failed on
// AADSTS7000215 every three minutes for an hour; the cluster stayed CPU-starved
// with 52 pods Pending, including keycloak-db, which every SSO consumer on the
// platform waits for (2026-10-04).
//
// That was the third independent resolution of one credential in this repo.
func TestCrossplaneCredentialsUseTheSharedResolver(t *testing.T) {
	b, err := os.ReadFile("crossplane_creds.go")
	if err != nil {
		t.Fatal(err)
	}
	var code strings.Builder
	for _, line := range strings.Split(string(b), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		code.WriteString(line + "\n")
	}
	src := code.String()
	if !strings.Contains(src, "config.ResolveSecret(") {
		t.Error("the Azure client secret for azure-credentials must come from config.ResolveSecret, " +
			"so clientSecretFile reaches the in-cluster controllers as well as the CLI")
	}
	if strings.Contains(src, `get(pcv.ClientSecret, "AZURE_CLIENT_SECRET"`) {
		t.Error("the Azure client secret is being resolved with a local rule again — that rule " +
			"cannot see clientSecretFile, which left the node autoscaler with a stale credential")
	}
}
