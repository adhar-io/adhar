package adharplatform

import (
	"os"
	"strings"
	"testing"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/platform/utils/files"
)

const certManagerManifests = "../../stack/packages/security/cert-manager/manifests/"

// Civo now gets a publicly trusted certificate, which took FOUR things agreeing.
// Any one of them missing leaves the platform serving a self-signed certificate
// while looking configured, which is how Civo behaved before:
//
//  1. civo in acmeDNS01Providers — or ACMEIssuer() picks the self-signed issuer
//     and nothing else matters
//  2. a civo branch in cluster-issuers.yaml.tmpl — or the issuer has no solver
//  3. the webhook solver actually installed — or the challenge has nowhere to go
//  4. the webhook NOT installed on other clouds — a failed APIService makes
//     `kubectl api-resources` noisy for every user of the cluster
func TestCivoGetsATrustedCertificatePath(t *testing.T) {
	if !(v1alpha1.BuildCustomizationSpec{DNSProvider: "civo"}).HasDNS01() {
		t.Fatal("civo must be an ACME DNS-01 provider, or ACMEIssuer() falls back to self-signed")
	}
	if got := (v1alpha1.BuildCustomizationSpec{DNSProvider: "civo"}).ACMEIssuer(); !strings.Contains(got, "letsencrypt") {
		t.Errorf("ACMEIssuer() for civo = %q, want the Let's Encrypt DNS issuer", got)
	}
}

// The issuer template must route Civo at the webhook, and every other provider at
// its own native solver.
func TestClusterIssuersRouteEachProviderAtItsSolver(t *testing.T) {
	raw, err := os.ReadFile(certManagerManifests + "cluster-issuers.yaml.tmpl")
	if err != nil {
		t.Fatalf("reading the template: %v", err)
	}
	for _, tc := range []struct{ provider, want string }{
		{"civo", "civo.webhook.okteto.com"},
		{"digitalocean", "digitalocean:"},
		{"aws", "route53:"},
		{"cloudflare", "cloudflare:"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			out, err := files.ApplyTemplate(raw, v1alpha1.BuildCustomizationSpec{
				Host: "hub.example.com", DNSProvider: tc.provider, Email: "a@example.com",
			})
			if err != nil {
				t.Fatalf("rendering for %s: %v", tc.provider, err)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Errorf("%s issuer is missing %q:\n%s", tc.provider, tc.want, out)
			}
			if tc.provider != "civo" && strings.Contains(string(out), "civo.webhook") {
				t.Errorf("%s must not reference the Civo webhook", tc.provider)
			}
		})
	}
}

// The webhook is an aggregated APIService. One that cannot serve makes every
// `kubectl api-resources` in the cluster print a discovery warning — the same
// trap kubescape set (see CLAUDE.md) — so it must appear ONLY on Civo.
func TestCivoWebhookIsInstalledOnlyForCivo(t *testing.T) {
	raw, err := os.ReadFile(certManagerManifests + "civo-dns-webhook.yaml.tmpl")
	if err != nil {
		t.Fatalf("reading the template: %v", err)
	}
	civo, err := files.ApplyTemplate(raw, v1alpha1.BuildCustomizationSpec{DNSProvider: "civo"})
	if err != nil {
		t.Fatalf("rendering for civo: %v", err)
	}
	for _, want := range []string{"kind: APIService", "v1alpha1.civo.webhook.okteto.com", "okteto/civo-webhook:0.5.4"} {
		if !strings.Contains(string(civo), want) {
			t.Errorf("the civo rendering is missing %q", want)
		}
	}
	// Pinned, not :latest — it is a third-party image in the certificate path.
	if strings.Contains(string(civo), "civo-webhook:latest") {
		t.Error("the webhook image must be pinned, not :latest")
	}

	for _, provider := range []string{"digitalocean", "aws", "gcp", "azure", "cloudflare", "none", ""} {
		out, err := files.ApplyTemplate(raw, v1alpha1.BuildCustomizationSpec{DNSProvider: provider})
		if err != nil {
			t.Fatalf("rendering for %q: %v", provider, err)
		}
		// "kind:" prefixed, because the header comment names APIService in prose
		// and that comment renders for every provider.
		if strings.Contains(string(out), "kind: APIService") {
			t.Errorf("dnsProvider=%q must not install the Civo webhook APIService", provider)
		}
		if strings.Contains(string(out), "kind: Deployment") {
			t.Errorf("dnsProvider=%q must not install the Civo webhook Deployment", provider)
		}
	}
}

// The chart grants get/watch on EVERY secret in the namespace, and this namespace
// holds the whole platform's credentials. The render narrows it to the one secret
// the webhook reads.
func TestCivoWebhookSecretAccessIsNarrowed(t *testing.T) {
	raw, err := os.ReadFile(certManagerManifests + "civo-dns-webhook.yaml.tmpl")
	if err != nil {
		t.Fatalf("reading the template: %v", err)
	}
	out, err := files.ApplyTemplate(raw, v1alpha1.BuildCustomizationSpec{DNSProvider: "civo"})
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	if !strings.Contains(string(out), "resourceNames") {
		t.Error("the secret-reader Role must name the one secret it needs, not every secret in adhar-system")
	}
	if !strings.Contains(string(out), v1alpha1.DNSProviderSecretName) {
		t.Errorf("the Role should name %q", v1alpha1.DNSProviderSecretName)
	}
}
