package up

import (
	"testing"

	"adhar-io/adhar/platform/config"
)

// cert-manager's azureDNS solver takes these INLINE — only the client secret may be
// a secretRef. Omitting resourceGroupName had the API server reject the whole
// ClusterIssuer, so the platform wildcard certificate stayed Ready=False with no
// Order ever created and the Gateway silently kept self-signed TLS (2026-09-26).
func TestResolveAzureDNSIdentifiers(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ConfigProviderConfig{
			"azure": {
				ClientID: "client-1",
				TenantID: "tenant-1",
				Config: map[string]interface{}{
					// Keys arrive lower-cased in practice, which is exactly what broke
					// the provider's own parsing — so the lookup must not be case- or
					// separator-sensitive.
					"subscriptionid":   "sub-1",
					"dnsresourcegroup": "dns-rg",
				},
			},
		},
	}
	sub, tenant, client, rg := resolveAzureDNSIdentifiers(cfg, dnsAzure)
	for _, tc := range []struct{ field, want, have string }{
		{"subscription", "sub-1", sub},
		{"tenant", "tenant-1", tenant},
		{"clientID", "client-1", client},
		{"resourceGroup", "dns-rg", rg},
	} {
		if tc.have != tc.want {
			t.Errorf("%s = %q, want %q", tc.field, tc.have, tc.want)
		}
	}
}

// The cluster's own resource group is the sensible fallback for the zone's group.
func TestResolveAzureDNSFallsBackToTheClusterResourceGroup(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ConfigProviderConfig{
			"azure": {Config: map[string]interface{}{
				"subscriptionid": "sub-1",
				"resourcegroup":  "adhar-rg",
			}},
		},
	}
	if _, _, _, rg := resolveAzureDNSIdentifiers(cfg, dnsAzure); rg != "adhar-rg" {
		t.Errorf("resourceGroup = %q, want the cluster group adhar-rg", rg)
	}
}

// Nothing is resolved for other DNS providers, so a GCP or DigitalOcean issuer is
// never handed stray Azure values.
func TestResolveAzureDNSIgnoresOtherProviders(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.ConfigProviderConfig{
		"azure": {ClientID: "client-1"},
	}}
	if s, tn, c, rg := resolveAzureDNSIdentifiers(cfg, dnsGCP); s != "" || tn != "" || c != "" || rg != "" {
		t.Errorf("expected nothing for a non-azure DNS provider, got %q %q %q %q", s, tn, c, rg)
	}
}
