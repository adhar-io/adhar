package up

import (
	"testing"

	"adhar-io/adhar/platform/config"
)

// cert-manager's route53 solver REQUIRES a region. Without one the Challenge sits
// `pending` forever on "Invalid Configuration: Missing Region", no
// _acme-challenge TXT record is written, and the wildcard certificate never
// issues — so every platform URL fails TLS with SSL_ERROR_SYSCALL while DNS, the
// load balancer and the Gateway are all demonstrably healthy. This is the same
// class of omission as DNSProject (GCP) and DNSAzure* (Azure), and it fails later
// and more confusingly than either, because the ClusterIssuer itself applies
// cleanly.
func TestDNSRegionComesFromTheAWSProvider(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.ConfigProviderConfig{
		"aws": {Region: "ap-southeast-1"},
	}}
	if got := resolveDNSRegion(cfg, dnsAWS); got != "ap-southeast-1" {
		t.Errorf("resolveDNSRegion = %q, want ap-southeast-1", got)
	}
}

// The region can also live in the provider's `config:` block, where keys arrive
// lower-cased — the lookup must tolerate that, as every other config read does.
func TestDNSRegionComesFromTheProviderConfigBlock(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.ConfigProviderConfig{
		"aws": {Config: map[string]interface{}{"region": "eu-west-1"}},
	}}
	if got := resolveDNSRegion(cfg, dnsAWS); got != "eu-west-1" {
		t.Errorf("resolveDNSRegion = %q, want eu-west-1", got)
	}
}

func TestDNSRegionFallsBackToTheEnvironment(t *testing.T) {
	t.Setenv("AWS_REGION", "us-east-2")
	cfg := &config.Config{Providers: map[string]config.ConfigProviderConfig{"aws": {}}}
	if got := resolveDNSRegion(cfg, dnsAWS); got != "us-east-2" {
		t.Errorf("resolveDNSRegion = %q, want us-east-2", got)
	}
}

// Only AWS uses it; the other solvers must not be handed a stray value.
func TestDNSRegionIsOnlyForRoute53(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.ConfigProviderConfig{
		"aws": {Region: "ap-southeast-1"},
	}}
	for _, p := range []string{dnsAzure, dnsGCP, dnsDigitalOcean, dnsCivo, ""} {
		if got := resolveDNSRegion(cfg, p); got != "" {
			t.Errorf("provider %q should get no region, got %q", p, got)
		}
	}
	if got := resolveDNSRegion(nil, dnsAWS); got != "" {
		t.Errorf("a nil config must not panic or invent a region, got %q", got)
	}
}
