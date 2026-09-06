package up

import (
	"context"
	"fmt"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/config"
)

// Platform edge DNS/TLS wiring.
//
// A production platform needs two things a local cluster does not: its
// hostnames published in a real DNS zone (external-dns) and a publicly trusted
// wildcard certificate for that zone (cert-manager, ACME DNS-01). Both need
// credentials for the DNS provider. Instead of hardcoding a provider anywhere
// in the stack, the CLI derives the provider from the environment's cloud (or
// globalSettings.dnsProvider), materialises its credentials once into the
// adhar-dns-provider Secret in the platform namespace, and records the choice
// on the AdharPlatform spec; the seeded stack templates (external-dns
// Deployment, cert-manager ClusterIssuers) and the cloud Gateway render from
// that spec. Local clusters (no DNS provider) keep the in-memory external-dns
// provider and the self-signed issuer.

// Canonical DNS provider names (BuildCustomizationSpec.DNSProvider values).
const (
	dnsDigitalOcean = "digitalocean"
	dnsAWS          = "aws"
	dnsGCP          = "gcp"
	dnsAzure        = "azure"
	dnsCivo         = "civo"
	dnsCloudflare   = "cloudflare"
)

// canonicalProvider maps the provider spellings accepted in config/CLI
// (do, doks, gke, eks, aks, k3s, …) to the canonical DNS provider names.
func canonicalProvider(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "digitalocean", "do", "doks":
		return dnsDigitalOcean
	case "aws", "eks":
		return dnsAWS
	case "gcp", "gke", "google":
		return dnsGCP
	case "azure", "aks":
		return dnsAzure
	case "civo", "k3s":
		return dnsCivo
	case dnsCloudflare:
		return dnsCloudflare
	}
	return ""
}

// resolveDNSProvider returns the edge DNS provider for an environment: the
// explicit globalSettings.dnsProvider when set ("none" disables edge DNS),
// otherwise the environment's cloud provider when that cloud has a DNS
// service, otherwise "" (no edge DNS).
func resolveDNSProvider(cfg *config.Config, cloudProvider string) string {
	if cfg != nil {
		switch v := strings.ToLower(strings.TrimSpace(cfg.GlobalSettings.DNSProvider)); v {
		case "":
		case "none", "off", "disabled":
			return ""
		default:
			return canonicalProvider(v)
		}
	}
	return canonicalProvider(cloudProvider)
}

// edgeDNSSecretData assembles the adhar-dns-provider Secret contents for a DNS
// provider from the configured provider credentials (config file first, then
// the provider's conventional environment variables). Keys follow what the
// consumers expect: cert-manager's DNS-01 solvers read provider-specific keys
// (access-token, secret-access-key, client-secret, …), external-dns reads its
// providers' environment variables (DO_TOKEN, AWS_*, CF_API_TOKEN, …) which the
// external-dns Deployment template sources from this Secret.
func edgeDNSSecretData(dnsProvider string, pc *config.ConfigProviderConfig) (map[string][]byte, error) {
	get := func(cfgVal string, envs ...string) string {
		if v := strings.TrimSpace(cfgVal); v != "" {
			return v
		}
		for _, e := range envs {
			if v := strings.TrimSpace(os.Getenv(e)); v != "" {
				return v
			}
		}
		return ""
	}
	var pcv config.ConfigProviderConfig
	if pc != nil {
		pcv = *pc
	}
	extra := func(key string) string {
		if pcv.Config == nil {
			return ""
		}
		if v, ok := pcv.Config[key].(string); ok {
			return strings.TrimSpace(v)
		}
		return ""
	}

	data := map[string][]byte{}
	switch dnsProvider {
	case dnsDigitalOcean:
		token := get(pcv.Token, "DIGITALOCEAN_TOKEN", "DIGITALOCEAN_ACCESS_TOKEN")
		if token == "" {
			return nil, fmt.Errorf("DigitalOcean DNS: no API token (providers.digitalocean.token or DIGITALOCEAN_TOKEN)")
		}
		data["access-token"] = []byte(token) // cert-manager digitalocean solver
		data["DO_TOKEN"] = []byte(token)     // external-dns digitalocean provider
	case dnsAWS:
		id := get(pcv.AccessKeyID, "AWS_ACCESS_KEY_ID")
		secret := get(pcv.SecretAccessKey, "AWS_SECRET_ACCESS_KEY")
		if id == "" || secret == "" {
			return nil, fmt.Errorf("Route53 DNS: static credentials required (providers.aws.accessKeyId/secretAccessKey or AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY)")
		}
		data["access-key-id"] = []byte(id)
		data["secret-access-key"] = []byte(secret) // cert-manager route53 solver
		data["AWS_ACCESS_KEY_ID"] = []byte(id)     // external-dns aws provider
		data["AWS_SECRET_ACCESS_KEY"] = []byte(secret)
		if region := get(pcv.Region, "AWS_REGION", "AWS_DEFAULT_REGION"); region != "" {
			data["region"] = []byte(region)
			data["AWS_DEFAULT_REGION"] = []byte(region)
		}
	case dnsGCP:
		key := strings.TrimSpace(pcv.ServiceAccountKey)
		if key == "" {
			path := get(pcv.ServiceAccountKeyFile, "GOOGLE_APPLICATION_CREDENTIALS")
			if path == "" {
				return nil, fmt.Errorf("Cloud DNS: a service account key is required (providers.gcp.serviceAccountKey[File] or GOOGLE_APPLICATION_CREDENTIALS)")
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("Cloud DNS: reading service account key: %w", err)
			}
			key = string(b)
		}
		project := get(pcv.ProjectID, "GOOGLE_PROJECT", "CLOUDSDK_CORE_PROJECT")
		if project == "" {
			return nil, fmt.Errorf("Cloud DNS: providers.gcp.projectId is required")
		}
		data["credentials.json"] = []byte(key) // cert-manager cloudDNS + external-dns (file mount)
		data["project"] = []byte(project)
	case dnsAzure:
		clientID := get(pcv.ClientID, "AZURE_CLIENT_ID")
		clientSecret := get(pcv.ClientSecret, "AZURE_CLIENT_SECRET")
		tenant := get(pcv.TenantID, "AZURE_TENANT_ID")
		subscription := get(extra("subscriptionId"), "AZURE_SUBSCRIPTION_ID")
		rg := get(extra("dnsResourceGroup"), "AZURE_DNS_RESOURCE_GROUP")
		if clientID == "" || clientSecret == "" || tenant == "" || subscription == "" || rg == "" {
			return nil, fmt.Errorf("Azure DNS: clientId, clientSecret, tenantId, config.subscriptionId and config.dnsResourceGroup (zone resource group) are required")
		}
		data["client-id"] = []byte(clientID)
		data["client-secret"] = []byte(clientSecret) // cert-manager azureDNS solver
		data["tenant-id"] = []byte(tenant)
		data["subscription-id"] = []byte(subscription)
		data["resource-group"] = []byte(rg)
		// external-dns azure provider reads a JSON config file.
		data["azure.json"] = []byte(fmt.Sprintf(
			`{"tenantId":%q,"subscriptionId":%q,"resourceGroup":%q,"aadClientId":%q,"aadClientSecret":%q}`,
			tenant, subscription, rg, clientID, clientSecret))
	case dnsCivo:
		token := get(pcv.Token, "CIVO_TOKEN", "CIVO_API_KEY")
		if token == "" {
			return nil, fmt.Errorf("Civo DNS: no API token (providers.civo.token or CIVO_TOKEN)")
		}
		data["CIVO_TOKEN"] = []byte(token) // external-dns civo provider (no cert-manager solver)
	case dnsCloudflare:
		token := get(extra("cloudflareApiToken"), "CLOUDFLARE_API_TOKEN", "CF_API_TOKEN")
		if token == "" {
			return nil, fmt.Errorf("Cloudflare DNS: no API token (CLOUDFLARE_API_TOKEN)")
		}
		data["api-token"] = []byte(token)    // cert-manager cloudflare solver
		data["CF_API_TOKEN"] = []byte(token) // external-dns cloudflare provider
	default:
		return nil, fmt.Errorf("unsupported DNS provider %q (digitalocean, aws, gcp, azure, civo, cloudflare)", dnsProvider)
	}
	return data, nil
}

// ensureEdgeDNSSecret creates/updates the adhar-dns-provider Secret for the
// platform's DNS provider. It is idempotent and never writes credentials
// anywhere but the cluster.
func ensureEdgeDNSSecret(ctx context.Context, c client.Client, dnsProvider string, pc *config.ConfigProviderConfig) error {
	data, err := edgeDNSSecretData(dnsProvider, pc)
	if err != nil {
		return err
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      v1alpha1.DNSProviderSecretName,
		Namespace: globals.AdharSystemNamespace,
	}}
	_, err = controllerutil.CreateOrUpdate(ctx, c, secret, func() error {
		if secret.Labels == nil {
			secret.Labels = map[string]string{}
		}
		secret.Labels["app.kubernetes.io/managed-by"] = "adhar"
		secret.Labels["adhar.io/dns-provider"] = dnsProvider
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = data
		return nil
	})
	if err != nil {
		return fmt.Errorf("ensuring %s secret: %w", v1alpha1.DNSProviderSecretName, err)
	}
	return nil
}
