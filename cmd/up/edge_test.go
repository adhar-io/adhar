package up

import (
	"testing"

	"adhar-io/adhar/platform/config"
)

func TestResolveDNSProvider(t *testing.T) {
	cases := []struct {
		name, global, cloud, want string
	}{
		{"derived from cloud", "", "digitalocean", "digitalocean"},
		{"cloud alias", "", "gke", "gcp"},
		{"cloud without DNS", "", "kind", ""},
		{"custom cloud, no override", "", "custom", ""},
		{"explicit override", "cloudflare", "custom", "cloudflare"},
		{"explicit alias", "DO", "aws", "digitalocean"},
		{"disabled", "none", "aws", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.GlobalSettings.DNSProvider = c.global
			if got := resolveDNSProvider(cfg, c.cloud); got != c.want {
				t.Errorf("resolveDNSProvider(%q, %q) = %q, want %q", c.global, c.cloud, got, c.want)
			}
		})
	}
	if got := resolveDNSProvider(nil, "azure"); got != "azure" {
		t.Errorf("nil config should still derive from cloud, got %q", got)
	}
}

func TestEdgeDNSSecretData(t *testing.T) {
	t.Setenv("DIGITALOCEAN_TOKEN", "")
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "")

	// Config token wins; both consumers get their key.
	data, err := edgeDNSSecretData("digitalocean", &config.ConfigProviderConfig{Token: "cfg-token"})
	if err != nil {
		t.Fatal(err)
	}
	if string(data["access-token"]) != "cfg-token" || string(data["DO_TOKEN"]) != "cfg-token" {
		t.Errorf("unexpected DO secret data: %v", data)
	}

	// Environment fallback (doctl's variable name too).
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "env-token")
	data, err = edgeDNSSecretData("digitalocean", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(data["DO_TOKEN"]) != "env-token" {
		t.Errorf("expected env token, got %v", data)
	}

	// Missing credentials are an error, not a silent no-op.
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "")
	if _, err := edgeDNSSecretData("digitalocean", nil); err == nil {
		t.Error("expected error without a DigitalOcean token")
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	if _, err := edgeDNSSecretData("aws", &config.ConfigProviderConfig{AccessKeyID: "only-id"}); err == nil {
		t.Error("expected error with incomplete AWS credentials")
	}
	if _, err := edgeDNSSecretData("route53", nil); err == nil {
		t.Error("expected error for an unknown provider name")
	}

	// Azure needs the zone's subscription and resource group from config.
	data, err = edgeDNSSecretData("azure", &config.ConfigProviderConfig{
		ClientID: "cid", ClientSecret: "sec", TenantID: "tid",
		Config: map[string]interface{}{"subscriptionId": "sub", "dnsResourceGroup": "rg"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(data["resource-group"]) != "rg" || len(data["azure.json"]) == 0 {
		t.Errorf("unexpected Azure secret data: %v", data)
	}
}
