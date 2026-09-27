package up

import (
	"context"
	"errors"
	"strings"
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
	data, err := edgeDNSSecretData(context.Background(), "digitalocean", &config.ConfigProviderConfig{Token: "cfg-token"})
	if err != nil {
		t.Fatal(err)
	}
	if string(data["access-token"]) != "cfg-token" || string(data["DO_TOKEN"]) != "cfg-token" {
		t.Errorf("unexpected DO secret data: %v", data)
	}

	// Environment fallback (doctl's variable name too).
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "env-token")
	data, err = edgeDNSSecretData(context.Background(), "digitalocean", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(data["DO_TOKEN"]) != "env-token" {
		t.Errorf("expected env token, got %v", data)
	}

	// Missing credentials are an error, not a silent no-op.
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "")
	if _, err := edgeDNSSecretData(context.Background(), "digitalocean", nil); err == nil {
		t.Error("expected error without a DigitalOcean token")
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	// Incomplete static credentials fall back to the AWS default credential chain,
	// because the provider itself authenticates that way: `adhar up` used to build
	// an entire cluster from ~/.aws/credentials and then fail at the edge DNS step
	// claiming credentials were missing. The resolver is stubbed so the assertion
	// does not depend on whether this machine has AWS configured.
	withChain := func(id, secret, token string, err error) func() {
		restore := awsStaticCredentials
		awsStaticCredentials = func(context.Context) (string, string, string, error) {
			return id, secret, token, err
		}
		return func() { awsStaticCredentials = restore }
	}

	restore := withChain("chain-id", "chain-secret", "", nil)
	awsData, err := edgeDNSSecretData(context.Background(), "aws", &config.ConfigProviderConfig{AccessKeyID: "only-id"})
	if err != nil {
		t.Fatalf("the chain should have supplied the missing secret: %v", err)
	}
	if string(awsData["access-key-id"]) != "chain-id" || string(awsData["AWS_SECRET_ACCESS_KEY"]) != "chain-secret" {
		t.Errorf("chain credentials unused: %q / %q", awsData["access-key-id"], awsData["AWS_SECRET_ACCESS_KEY"])
	}
	if _, ok := awsData["AWS_SESSION_TOKEN"]; ok {
		t.Error("a long-lived key must not publish an empty session token")
	}
	restore()

	// Temporary credentials work but expire, after which external-dns silently
	// stops publishing and certificates stop renewing — carry the token so the
	// caller can warn.
	restore = withChain("temp-id", "temp-secret", "temp-token", nil)
	awsData, err = edgeDNSSecretData(context.Background(), "aws", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(awsData["AWS_SESSION_TOKEN"]) != "temp-token" {
		t.Errorf("session token = %q, want temp-token", awsData["AWS_SESSION_TOKEN"])
	}
	restore()

	// With nothing available anywhere it must still fail, naming every place a
	// credential can come from.
	restore = withChain("", "", "", errors.New("no credential provider was configured"))
	_, err = edgeDNSSecretData(context.Background(), "aws", &config.ConfigProviderConfig{AccessKeyID: "only-id"})
	if err == nil {
		t.Error("expected an error when no credentials are available at all")
	} else {
		for _, want := range []string{"accessKeyId", "AWS_ACCESS_KEY_ID", ".aws/credentials"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error should mention %q: %v", want, err)
			}
		}
	}
	restore()
	if _, err := edgeDNSSecretData(context.Background(), "route53", nil); err == nil {
		t.Error("expected error for an unknown provider name")
	}

	// Azure needs the zone's subscription and resource group from config.
	data, err = edgeDNSSecretData(context.Background(), "azure", &config.ConfigProviderConfig{
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
