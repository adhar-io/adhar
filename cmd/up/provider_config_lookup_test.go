package up

import "testing"

// Keys in a provider's `config:` map arrive LOWER-CASED. An exact-case lookup
// therefore found nothing, and the consequences were all silent: the Azure
// provider built a cluster in a resource group nobody asked for, the edge DNS
// step reported five values missing while the file set them, and
// `azure-credentials` was never written — which left the node autoscaler unable
// to add a worker on a cluster whose pods were Pending for capacity.
func TestProviderConfigStringMatchesHoweverTheKeyIsSpelled(t *testing.T) {
	cfg := map[string]interface{}{
		"subscriptionid":   "sub-123",
		"dnsresourcegroup": "adhar-rg",
	}
	for _, spelling := range []string{"subscriptionId", "subscription_id", "SUBSCRIPTION-ID", "subscriptionid"} {
		if got := providerConfigString(cfg, spelling); got != "sub-123" {
			t.Errorf("providerConfigString(%q) = %q, want sub-123", spelling, got)
		}
	}
	if got := providerConfigString(cfg, "dnsResourceGroup"); got != "adhar-rg" {
		t.Errorf("dnsResourceGroup = %q, want adhar-rg", got)
	}
}

// A decoded config's numbers are not strings; a string-only assertion dropped
// them and fell back to a built-in default.
func TestProviderConfigStringRendersNonStringScalars(t *testing.T) {
	cfg := map[string]interface{}{"disksizegb": 256, "usemanagedk8s": true, "count": float64(4)}
	if got := providerConfigString(cfg, "diskSizeGb"); got != "256" {
		t.Errorf("diskSizeGb = %q, want 256", got)
	}
	if got := providerConfigString(cfg, "useManagedK8s"); got != "true" {
		t.Errorf("useManagedK8s = %q, want true", got)
	}
	if got := providerConfigString(cfg, "count"); got != "4" {
		t.Errorf("count = %q, want 4 (not 4.0)", got)
	}
}

func TestProviderConfigStringHandlesAnEmptyMap(t *testing.T) {
	if got := providerConfigString(nil, "anything"); got != "" {
		t.Errorf("nil config should yield %q, got %q", "", got)
	}
}
