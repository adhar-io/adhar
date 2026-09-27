package up

import (
	"context"
	"fmt"
	"os"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/logger"
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

// resolveAzureDNSIdentifiers returns the four values cert-manager's azureDNS
// solver requires inline: subscription, tenant, client id and the resource group
// holding the zone.
//
// Same reason resolveDNSProject exists. cert-manager rejects an azureDNS solver
// without resourceGroupName, so the ClusterIssuer never applies, the wildcard
// certificate stays Ready=False with no Order created, and the Gateway silently
// keeps its self-signed certificate.
//
// The lookups mirror the DNS secret's: provider config first, then the standard
// AZURE_* environment variables, and keys are matched ignoring case and separators
// because the provider config map arrives lower-cased.
func resolveAzureDNSIdentifiers(cfg *config.Config, dnsProvider string) (subscription, tenant, clientID, resourceGroup string) {
	if dnsProvider != dnsAzure || cfg == nil {
		return "", "", "", ""
	}
	normalise := func(k string) string {
		out := make([]rune, 0, len(k))
		for _, r := range strings.ToLower(k) {
			if r == '_' || r == '-' || r == ' ' || r == '.' {
				continue
			}
			out = append(out, r)
		}
		return string(out)
	}
	for name, pc := range cfg.Providers {
		if !strings.EqualFold(name, "azure") {
			continue
		}
		m := pc.ToProviderMap()
		top := func(key string) string {
			if v, ok := m[key].(string); ok {
				return strings.TrimSpace(v)
			}
			return ""
		}
		clientID = top("clientId")
		tenant = top("tenantId")
		if section, ok := m["config"].(map[string]interface{}); ok {
			want := map[string]*string{
				"subscriptionid":   &subscription,
				"dnsresourcegroup": &resourceGroup,
				"tenantid":         &tenant,
				"clientid":         &clientID,
			}
			for k, v := range section {
				if target, ok := want[normalise(k)]; ok {
					if sv, ok := v.(string); ok && strings.TrimSpace(sv) != "" && *target == "" {
						*target = strings.TrimSpace(sv)
					}
				}
			}
			// The cluster's own resource group is the sensible fallback for the zone.
			if resourceGroup == "" {
				for k, v := range section {
					if normalise(k) == "resourcegroup" {
						if sv, ok := v.(string); ok {
							resourceGroup = strings.TrimSpace(sv)
						}
					}
				}
			}
		}
		break
	}
	if subscription == "" {
		subscription = strings.TrimSpace(os.Getenv("AZURE_SUBSCRIPTION_ID"))
	}
	if tenant == "" {
		tenant = strings.TrimSpace(os.Getenv("AZURE_TENANT_ID"))
	}
	if clientID == "" {
		clientID = strings.TrimSpace(os.Getenv("AZURE_CLIENT_ID"))
	}
	if resourceGroup == "" {
		resourceGroup = strings.TrimSpace(os.Getenv("AZURE_DNS_RESOURCE_GROUP"))
	}
	return subscription, tenant, clientID, resourceGroup
}

// resolveDNSProject returns the cloud project that owns the DNS zone. Only
// Google Cloud needs it, and cert-manager's cloudDNS solver REQUIRES it — a
// ClusterIssuer without `project` is rejected by the API server, so the DNS-01
// issuer never applies and the wildcard certificate is never issued.
func resolveDNSProject(cfg *config.Config, dnsProvider string) string {
	if dnsProvider != dnsGCP || cfg == nil {
		return ""
	}
	for name, pc := range cfg.Providers {
		if !strings.EqualFold(name, "gcp") {
			continue
		}
		if id := pc.ToProviderMap()["projectId"]; id != nil {
			if s, ok := id.(string); ok && s != "" {
				return s
			}
		}
		if section, ok := pc.ToProviderMap()["config"].(map[string]interface{}); ok {
			if s, ok := section["project_id"].(string); ok && s != "" {
				return s
			}
		}
	}
	// Same environment fallbacks the DNS secret uses.
	for _, env := range []string{"GOOGLE_PROJECT", "CLOUDSDK_CORE_PROJECT"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v
		}
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
func edgeDNSSecretData(ctx context.Context, dnsProvider string, pc *config.ConfigProviderConfig) (map[string][]byte, error) {
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
	// Keys in the provider's `config:` map arrive LOWER-CASED, so an exact-case
	// lookup for "subscriptionId" or "dnsResourceGroup" never matched and the edge
	// DNS step reported all five values missing while the file plainly set them
	// (Azure, 2026-09-26). providerConfigString matches ignoring case and
	// separators, and renders non-string scalars too.
	extra := func(key string) string {
		return providerConfigString(pcv.Config, key)
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
		sessionToken := get("", "AWS_SESSION_TOKEN")
		if id == "" || secret == "" {
			// Fall back to the AWS default credential chain — the shared
			// credentials file, a named profile, SSO, or an instance role.
			//
			// This has to resolve to a static key pair because the consumers are
			// IN-CLUSTER: external-dns and cert-manager's route53 solver read a
			// Kubernetes Secret and cannot see ~/.aws/credentials. But requiring
			// the operator to ALSO export the keys as environment variables was
			// needless friction and an easy trap: the AWS provider itself
			// authenticates through the default chain, so `adhar up` would create
			// the whole cluster from ~/.aws/credentials and then fail at the edge
			// DNS step complaining that credentials were missing (measured on a
			// real bring-up, 2026-09-27). Resolve the same chain here instead.
			resolvedID, resolvedSecret, resolvedToken, err := awsStaticCredentials(ctx)
			if err != nil {
				return nil, fmt.Errorf("Route53 DNS: no usable AWS credentials — set providers.aws.accessKeyId/secretAccessKey, "+
					"or AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY, or configure the AWS CLI (~/.aws/credentials): %w", err)
			}
			id, secret, sessionToken = resolvedID, resolvedSecret, resolvedToken
		}
		if sessionToken != "" {
			// Temporary credentials (SSO, assumed role) DO work, but only until
			// they expire — after which external-dns stops publishing records and
			// certificate renewal fails, quietly, weeks later. Say so now.
			logger.Warn("Route53 DNS is using TEMPORARY AWS credentials; external-dns and certificate renewal " +
				"will fail when they expire. Use a long-lived key for the platform's own DNS identity.")
			data["AWS_SESSION_TOKEN"] = []byte(sessionToken)
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
	data, err := edgeDNSSecretData(ctx, dnsProvider, pc)
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

// awsStaticCredentials is the indirection tests replace. Without it, asserting
// what happens when no credentials exist would depend on whether the machine
// running the test happens to have ~/.aws/credentials — which is exactly the
// kind of environment-dependent test that passes locally and fails in CI.
var awsStaticCredentials = resolveAWSStaticCredentials

// resolveAWSStaticCredentials retrieves a concrete key pair from the AWS default
// credential chain (shared credentials file, named profile, SSO cache, instance
// role) so the platform can hand it to its in-cluster DNS consumers.
//
// external-dns and cert-manager's route53 solver read a Kubernetes Secret, so
// they need real key material; they cannot resolve a profile themselves. Reading
// the chain here means an operator who has already configured the AWS CLI does
// not have to restate the same keys as environment variables just to satisfy
// this one step.
//
// The returned session token is empty for long-lived keys and set for temporary
// ones; the caller warns about the latter, because expiry shows up much later as
// DNS records that stop updating and certificates that stop renewing.
func resolveAWSStaticCredentials(ctx context.Context) (id, secret, sessionToken string, err error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return "", "", "", fmt.Errorf("loading AWS configuration: %w", err)
	}
	if cfg.Credentials == nil {
		return "", "", "", fmt.Errorf("no credential provider was configured")
	}
	creds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return "", "", "", fmt.Errorf("retrieving credentials: %w", err)
	}
	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
		return "", "", "", fmt.Errorf("the credential chain returned an empty key pair")
	}
	return creds.AccessKeyID, creds.SecretAccessKey, creds.SessionToken, nil
}

// resolveDNSRegion returns the AWS region cert-manager's route53 solver needs.
//
// Only AWS uses it, and it is REQUIRED there: without a region the solver cannot
// resolve the Route 53 endpoint at all, so the ACME Challenge sits `pending`
// indefinitely on "Invalid Configuration: Missing Region", no _acme-challenge
// TXT record is written, and the platform's wildcard certificate never issues.
// The visible symptom is every URL failing TLS while DNS, the load balancer and
// the Gateway are all healthy — the reason appears only in the cert-manager pod's
// log (AWS, 2026-09-27).
//
// Resolution order matches resolveDNSProject: the provider's own configuration
// first, then the conventional environment variables the DNS secret also reads.
func resolveDNSRegion(cfg *config.Config, dnsProvider string) string {
	if dnsProvider != dnsAWS || cfg == nil {
		return ""
	}
	for name, pc := range cfg.Providers {
		if !strings.EqualFold(name, "aws") {
			continue
		}
		if r := strings.TrimSpace(pc.Region); r != "" {
			return r
		}
		m := pc.ToProviderMap()
		if r, ok := m["region"].(string); ok && strings.TrimSpace(r) != "" {
			return strings.TrimSpace(r)
		}
		if section, ok := m["config"].(map[string]interface{}); ok {
			if r := providerConfigString(section, "region"); r != "" {
				return r
			}
		}
	}
	for _, env := range []string{"AWS_REGION", "AWS_DEFAULT_REGION"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v
		}
	}
	return ""
}
