package azure

import (
	"strings"
	"testing"

	provider "adhar-io/adhar/platform/providers"
	"adhar-io/adhar/platform/types"
)

// The credential must be checked before anything billable is created.
//
// azureCloudConfig already refuses to write an azure.json that cannot
// authenticate, but it runs during cloud integration — after the VNet, the NSG,
// the public IPs and every VM exist. A live run on 2026-10-04 failed at 3m50s
// having created three VMs (Standard_E2bds_v5 + 2 × Standard_E4bds_v5) and
// three public IPs, which kept billing after the command exited:
//
//	✖  Cloud cluster
//	✖ prod: failed to create azure cluster: Azure cloud integration:
//	  azure.json would have no usable credential
//
// Nothing about the condition needs a network call or a created resource, so
// learning it late is pure waste. `az login` is what makes this easy to hit: it
// is enough to CREATE the cluster, so everything up to that point succeeds.
func TestPreflightRejectsAnAzureCLIOnlyCredential(t *testing.T) {
	p := &Provider{config: &Config{
		SubscriptionID: "sub", TenantID: "tenant", ClientID: "client",
		ClientSecret: "", UseAzureCLI: true, Location: "centralindia", ResourceGroup: "adhar-rg",
	}}
	got := p.preflightCloudCredential()
	if got.Status != provider.CheckFail {
		t.Fatalf("an az-login-only setup must fail preflight, got %v (%s)", got.Status, got.Detail)
	}
	// The fix has to be actionable without a trip to the Azure docs.
	for _, want := range []string{"az ad sp create-for-rbac", "clientSecret", "/subscriptions/sub", "adhar-rg"} {
		if !strings.Contains(got.Fix, want) {
			t.Errorf("the fix must name the command and the scope; missing %q in: %s", want, got.Fix)
		}
	}
	if !strings.Contains(got.Detail, "load balancer") {
		t.Errorf("the detail must say what actually breaks, got: %s", got.Detail)
	}
}

// useManagedIdentity passes azureCloudConfig but cannot work on a cluster this
// provider builds: nothing assigns an identity to the VMs it creates or grants
// it a role — only the AKS path does (managed.go). Accepting the flag trades a
// fast, clear failure for a cloud-controller-manager that crash-loops after the
// entire bootstrap has run, which is strictly worse.
func TestPreflightRejectsManagedIdentityOnSelfManagedVMs(t *testing.T) {
	p := &Provider{config: &Config{
		SubscriptionID: "sub", TenantID: "tenant", Location: "centralindia",
		ResourceGroup: "adhar-rg", UseManagedIdentity: true,
	}}
	got := p.preflightCloudCredential()
	if got.Status != provider.CheckFail {
		t.Fatalf("managed identity is not implemented for self-managed VMs and must fail preflight, "+
			"got %v (%s)", got.Status, got.Detail)
	}
	if !strings.Contains(got.Detail, "does not assign a managed identity") {
		t.Errorf("the detail must explain why the flag is not enough, got: %s", got.Detail)
	}
	// If identity assignment is ever implemented, this test should be the thing
	// that fails and gets deleted — so point at what would have to change.
	if !strings.Contains(got.Fix, "aks") {
		t.Errorf("the fix should offer the mode where Azure does assign an identity, got: %s", got.Fix)
	}
}

// A complete service principal is the supported path and must pass.
func TestPreflightAcceptsAServicePrincipal(t *testing.T) {
	p := &Provider{config: &Config{
		SubscriptionID: "sub", TenantID: "tenant", ClientID: "client", ClientSecret: "secret",
		Location: "centralindia", ResourceGroup: "adhar-rg",
	}}
	if got := p.preflightCloudCredential(); got.Status != provider.CheckPass {
		t.Errorf("a full service principal must pass, got %v (%s)", got.Status, got.Detail)
	}
}

// A half-configured principal is the likeliest mistake once someone starts
// setting these fields, and it fails exactly as opaquely as an empty one.
func TestPreflightRejectsAPartialServicePrincipal(t *testing.T) {
	for _, tc := range []struct {
		name             string
		clientID, tenant string
	}{
		{"no clientId", "", "tenant"},
		{"no tenantId", "client", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &Provider{config: &Config{
				SubscriptionID: "sub", TenantID: tc.tenant, ClientID: tc.clientID,
				ClientSecret: "secret", Location: "centralindia", ResourceGroup: "adhar-rg",
			}}
			if got := p.preflightCloudCredential(); got.Status != provider.CheckFail {
				t.Errorf("all three fields are required, got %v (%s)", got.Status, got.Detail)
			}
		})
	}
}

// AKS is exempt: Azure assigns the cluster a system-assigned identity and this
// provider writes no azure.json for it.
func TestPreflightExemptsManagedAKS(t *testing.T) {
	p := &Provider{config: &Config{
		SubscriptionID: "sub", TenantID: "tenant", Location: "centralindia",
		ResourceGroup: "adhar-rg", ClusterMode: clusterModeAKS,
	}}
	if got := p.preflightCloudCredential(); got.Status != provider.CheckPass {
		t.Errorf("managed AKS needs no service principal of ours, got %v (%s)", got.Status, got.Detail)
	}
}

// The check must be wired into Preflight, not merely exist. A check nobody calls
// is the same as no check, and this one exists specifically to run early.
func TestPreflightRunsTheCredentialCheck(t *testing.T) {
	// No Azure clients are set, so the subscription/quota probes are skipped and
	// only the configuration-derived checks run — which is what this asserts.
	// The spec must be non-nil: plannedVMSize reads it without a guard.
	p := &Provider{config: &Config{
		SubscriptionID: "sub", TenantID: "tenant", Location: "centralindia", UseAzureCLI: true,
	}}
	var found bool
	for _, c := range p.Preflight(t.Context(), &types.ClusterSpec{}) {
		if c.Name == "cloud-provider credential" {
			found = true
			if c.Status != provider.CheckFail {
				t.Errorf("expected the credential check to fail for an az-login-only config, got %v", c.Status)
			}
		}
	}
	if !found {
		t.Error("Preflight must include the cloud-provider credential check — otherwise the failure " +
			"moves back to after the VMs are created and billing")
	}
}
