package up

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/config"
)

// Crossplane cloud-provider credentials.
//
// The control plane ships a ProviderConfig per cloud
// (platform/controlplane/configuration/providers/cloud/*-providerconfig.yaml),
// each reading its credentials from the Secret `<provider>-credentials` in the
// platform namespace. Nothing in Git carries a credential: the CLI materialises
// the Secret once from the same configuration the cluster itself was created
// with (providers.<name> in config.yaml, then the provider's conventional
// environment variables), exactly like the edge DNS credentials. Without it
// every CompositeCluster / CompositeDatabase on that cloud would sit at
// "credentials not found".

// crossplaneCredentialSecretName returns the Secret name the ProviderConfigs
// reference for a canonical provider name ("" when the provider has none).
func crossplaneCredentialSecretName(provider string) string {
	switch provider {
	case dnsDigitalOcean, dnsAWS, dnsGCP, dnsAzure, dnsCivo:
		return provider + "-credentials"
	default:
		return ""
	}
}

// crossplaneCredentialData assembles the Secret contents each provider's
// ProviderConfig expects (key names are fixed by those manifests).
func crossplaneCredentialData(provider string, pc *config.ConfigProviderConfig) (map[string][]byte, error) {
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

	switch provider {
	case dnsDigitalOcean:
		token := get(pcv.Token, "DIGITALOCEAN_TOKEN", "DIGITALOCEAN_ACCESS_TOKEN")
		if token == "" {
			return nil, fmt.Errorf("DigitalOcean: no API token (providers.digitalocean.token or DIGITALOCEAN_TOKEN)")
		}
		// provider-upjet-digitalocean json.Unmarshals the credential value
		// straight into the Terraform provider configuration, so it needs a
		// JSON object; `token` stays for the CLI/autoscaler and anything else
		// reading the raw value.
		creds, err := json.Marshal(map[string]string{"token": token})
		if err != nil {
			return nil, err
		}
		return map[string][]byte{"token": []byte(token), "credentials": creds}, nil
	case dnsCivo:
		token := get(pcv.Token, "CIVO_TOKEN", "CIVO_API_KEY")
		if token == "" {
			return nil, fmt.Errorf("Civo: no API key (providers.civo.token or CIVO_TOKEN)")
		}
		return map[string][]byte{"token": []byte(token)}, nil
	case dnsAWS:
		id := get(pcv.AccessKeyID, "AWS_ACCESS_KEY_ID")
		secret := get(pcv.SecretAccessKey, "AWS_SECRET_ACCESS_KEY")
		if id == "" || secret == "" {
			return nil, fmt.Errorf("AWS: static credentials required (providers.aws.accessKeyId/secretAccessKey or AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY)")
		}
		ini := fmt.Sprintf("[default]\naws_access_key_id = %s\naws_secret_access_key = %s\n", id, secret)
		return map[string][]byte{"credentials": []byte(ini)}, nil
	case dnsGCP:
		key := strings.TrimSpace(pcv.ServiceAccountKey)
		if key == "" {
			path := get(pcv.ServiceAccountKeyFile, "GOOGLE_APPLICATION_CREDENTIALS")
			if path == "" {
				return nil, fmt.Errorf("GCP: service account key required (providers.gcp.serviceAccountKey[File] or GOOGLE_APPLICATION_CREDENTIALS)")
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("GCP: reading service account key: %w", err)
			}
			key = string(b)
		}
		return map[string][]byte{"credentials": []byte(key)}, nil
	case dnsAzure:
		creds := map[string]string{
			"clientId":       get(pcv.ClientID, "AZURE_CLIENT_ID", "ARM_CLIENT_ID"),
			"clientSecret":   get(pcv.ClientSecret, "AZURE_CLIENT_SECRET", "ARM_CLIENT_SECRET"),
			"tenantId":       get(pcv.TenantID, "AZURE_TENANT_ID", "ARM_TENANT_ID"),
			"subscriptionId": get(extra("subscriptionId"), "AZURE_SUBSCRIPTION_ID", "ARM_SUBSCRIPTION_ID"),
		}
		for k, v := range creds {
			if v == "" {
				return nil, fmt.Errorf("Azure: %s required (providers.azure or AZURE_* environment)", k)
			}
		}
		b, err := json.Marshal(creds)
		if err != nil {
			return nil, err
		}
		return map[string][]byte{"credentials": b}, nil
	default:
		return nil, nil
	}
}

// ensureCrossplaneCredentialSecret materialises the cloud's Crossplane
// credentials Secret in the platform namespace (create or update, labelled).
// Providers without Crossplane packages (kind, custom) are a no-op.
func ensureCrossplaneCredentialSecret(ctx context.Context, c client.Client, provider string, pc *config.ConfigProviderConfig) error {
	name := crossplaneCredentialSecretName(provider)
	if name == "" {
		return nil
	}
	data, err := crossplaneCredentialData(provider, pc)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: globals.AdharSystemNamespace}}
	_, err = controllerutil.CreateOrUpdate(ctx, c, secret, func() error {
		if secret.Labels == nil {
			secret.Labels = map[string]string{}
		}
		secret.Labels["adhar.io/component"] = "crossplane-credentials"
		secret.Labels["adhar.io/provider"] = provider
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = data
		return nil
	})
	if err != nil {
		return fmt.Errorf("materialising Crossplane credentials %s: %w", name, err)
	}
	return nil
}
