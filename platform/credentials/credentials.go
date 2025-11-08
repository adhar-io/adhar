package credentials

import (
	"context"
	"fmt"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Provider represents a cloud provider that requires credentials
type Provider string

const (
	ProviderAzure        Provider = "azure"
	ProviderDigitalOcean Provider = "digitalocean"
	ProviderCivo         Provider = "civo"
	ProviderAWS          Provider = "aws"
	ProviderGCP          Provider = "gcp"
)

// CredentialSource represents where credentials can be sourced from
type CredentialSource string

const (
	SourceEnvironment CredentialSource = "environment"
	SourceSecret      CredentialSource = "secret"
	SourceFile        CredentialSource = "file"
)

// Credential represents provider credentials
type Credential struct {
	Provider  Provider
	Source    CredentialSource
	Data      map[string]string
	SecretRef *SecretReference
}

// SecretReference points to a Kubernetes secret
type SecretReference struct {
	Name      string
	Namespace string
	Key       string
}

// CredentialManager handles credential discovery and validation
type CredentialManager struct {
	k8sClient kubernetes.Interface
}

// NewCredentialManager creates a new credential manager
func NewCredentialManager(config *rest.Config) (*CredentialManager, error) {
	if config == nil {
		// If no config is provided, try to get in-cluster config
		var err error
		config, err = rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to get kubernetes config: %w", err)
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	return &CredentialManager{
		k8sClient: clientset,
	}, nil
}

// DiscoverCredentials attempts to discover credentials for a provider from multiple sources
func (cm *CredentialManager) DiscoverCredentials(ctx context.Context, provider Provider) (*Credential, error) {
	// Try environment variables first
	if cred := cm.discoverFromEnvironment(provider); cred != nil {
		return cred, nil
	}

	// Try common secret names
	if cred, err := cm.discoverFromSecrets(ctx, provider); err == nil && cred != nil {
		return cred, nil
	}

	// Try credential files
	if cred := cm.discoverFromFiles(provider); cred != nil {
		return cred, nil
	}

	return nil, fmt.Errorf("no credentials found for provider %s", provider)
}

// discoverFromEnvironment discovers credentials from environment variables
func (cm *CredentialManager) discoverFromEnvironment(provider Provider) *Credential {
	var envVars map[string]string
	var requiredKeys []string

	switch provider {
	case ProviderAzure:
		envVars = map[string]string{
			"clientId":       os.Getenv("AZURE_CLIENT_ID"),
			"clientSecret":   os.Getenv("AZURE_CLIENT_SECRET"),
			"tenantId":       os.Getenv("AZURE_TENANT_ID"),
			"subscriptionId": os.Getenv("AZURE_SUBSCRIPTION_ID"),
		}
		requiredKeys = []string{"clientId", "clientSecret", "tenantId", "subscriptionId"}

	case ProviderDigitalOcean:
		token := os.Getenv("DIGITALOCEAN_TOKEN")
		if token == "" {
			token = os.Getenv("DIGITALOCEAN_ACCESS_TOKEN")
		}
		if token != "" {
			envVars = map[string]string{"token": token}
			requiredKeys = []string{"token"}
		}

	case ProviderCivo:
		token := os.Getenv("CIVO_TOKEN")
		if token == "" {
			token = os.Getenv("CIVO_API_KEY")
		}
		if token != "" {
			envVars = map[string]string{"token": token}
			requiredKeys = []string{"token"}
		}

	case ProviderAWS:
		envVars = map[string]string{
			"accessKeyId":     os.Getenv("AWS_ACCESS_KEY_ID"),
			"secretAccessKey": os.Getenv("AWS_SECRET_ACCESS_KEY"),
			"region":          os.Getenv("AWS_REGION"),
		}
		requiredKeys = []string{"accessKeyId", "secretAccessKey"}

	case ProviderGCP:
		creds := os.Getenv("GOOGLE_CREDENTIALS")
		if creds == "" {
			creds = os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
		}
		if creds != "" {
			envVars = map[string]string{"credentials": creds}
			requiredKeys = []string{"credentials"}
		}
	}

	// Validate all required keys are present
	if envVars != nil {
		allPresent := true
		for _, key := range requiredKeys {
			if val, exists := envVars[key]; !exists || strings.TrimSpace(val) == "" {
				allPresent = false
				break
			}
		}

		if allPresent {
			return &Credential{
				Provider: provider,
				Source:   SourceEnvironment,
				Data:     envVars,
			}
		}
	}

	return nil
}

// discoverFromSecrets discovers credentials from Kubernetes secrets
func (cm *CredentialManager) discoverFromSecrets(ctx context.Context, provider Provider) (*Credential, error) {
	// Common secret names for each provider
	var secretNames []string
	var keyMappings map[string]string

	switch provider {
	case ProviderAzure:
		secretNames = []string{
			"azure-credentials",
			"crossplane-azure-provider-creds",
			"azure-provider-secret",
		}
		keyMappings = map[string]string{
			"clientId":       "AZURE_CLIENT_ID",
			"clientSecret":   "AZURE_CLIENT_SECRET",
			"tenantId":       "AZURE_TENANT_ID",
			"subscriptionId": "AZURE_SUBSCRIPTION_ID",
		}

	case ProviderDigitalOcean:
		secretNames = []string{
			"digitalocean-credentials",
			"crossplane-digitalocean-provider-creds",
			"do-provider-secret",
		}
		keyMappings = map[string]string{
			"token": "DIGITALOCEAN_TOKEN",
		}

	case ProviderCivo:
		secretNames = []string{
			"civo-credentials",
			"crossplane-civo-provider-creds",
			"civo-provider-secret",
		}
		keyMappings = map[string]string{
			"token": "CIVO_TOKEN",
		}

	case ProviderAWS:
		secretNames = []string{
			"aws-credentials",
			"crossplane-aws-provider-creds",
			"aws-provider-secret",
		}
		keyMappings = map[string]string{
			"accessKeyId":     "AWS_ACCESS_KEY_ID",
			"secretAccessKey": "AWS_SECRET_ACCESS_KEY",
		}

	case ProviderGCP:
		secretNames = []string{
			"gcp-credentials",
			"crossplane-gcp-provider-creds",
			"gcp-provider-secret",
		}
		keyMappings = map[string]string{
			"credentials": "GOOGLE_CREDENTIALS",
		}
	}

	// Try common namespaces
	namespaces := []string{"crossplane-system", "default", "kube-system"}

	for _, ns := range namespaces {
		for _, secretName := range secretNames {
			secret, err := cm.k8sClient.CoreV1().Secrets(ns).Get(ctx, secretName, metav1.GetOptions{})
			if err != nil {
				continue
			}

			// Extract credentials from secret
			data := make(map[string]string)
			for logicalKey, secretKey := range keyMappings {
				if val, exists := secret.Data[secretKey]; exists {
					data[logicalKey] = string(val)
				}
			}

			// Validate we got all required keys
			if len(data) == len(keyMappings) {
				return &Credential{
					Provider: provider,
					Source:   SourceSecret,
					Data:     data,
					SecretRef: &SecretReference{
						Name:      secretName,
						Namespace: ns,
					},
				}, nil
			}
		}
	}

	return nil, fmt.Errorf("no valid secrets found for provider %s", provider)
}

// discoverFromFiles discovers credentials from common file locations
func (cm *CredentialManager) discoverFromFiles(provider Provider) *Credential {
	var filePaths []string
	var parser func([]byte) (map[string]string, error)

	switch provider {
	case ProviderAzure:
		filePaths = []string{
			os.ExpandEnv("$HOME/.azure/credentials.json"),
			"/etc/kubernetes/azure.json",
		}
		parser = parseAzureCredentials

	case ProviderGCP:
		filePaths = []string{
			os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"),
			os.ExpandEnv("$HOME/.config/gcloud/application_default_credentials.json"),
		}
		parser = parseGCPCredentials

	case ProviderAWS:
		filePaths = []string{
			os.ExpandEnv("$HOME/.aws/credentials"),
		}
		parser = parseAWSCredentials
	}

	for _, path := range filePaths {
		if path == "" {
			continue
		}

		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		if parsed, err := parser(data); err == nil && len(parsed) > 0 {
			return &Credential{
				Provider: provider,
				Source:   SourceFile,
				Data:     parsed,
			}
		}
	}

	return nil
}

// GetOrCreateSecret retrieves or creates a Kubernetes secret with credentials
func (cm *CredentialManager) GetOrCreateSecret(ctx context.Context, cred *Credential, namespace, name string) (*SecretReference, error) {
	if cred.SecretRef != nil {
		// Secret already exists, return reference
		return cred.SecretRef, nil
	}

	// Create secret data
	secretData := make(map[string][]byte)
	for key, value := range cred.Data {
		secretData[key] = []byte(value)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "adhar-control-plane",
				"adhar.io/provider":            string(cred.Provider),
			},
		},
		Data: secretData,
		Type: corev1.SecretTypeOpaque,
	}

	// Try to create the secret
	_, err := cm.k8sClient.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
	if err != nil {
		// If secret exists, try to update it
		_, err = cm.k8sClient.CoreV1().Secrets(namespace).Update(ctx, secret, metav1.UpdateOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to create or update secret: %w", err)
		}
	}

	return &SecretReference{
		Name:      name,
		Namespace: namespace,
	}, nil
}

// ValidateCredentials performs basic validation of credentials
func (cm *CredentialManager) ValidateCredentials(cred *Credential) error {
	if cred == nil {
		return fmt.Errorf("credential is nil")
	}

	if cred.Data == nil || len(cred.Data) == 0 {
		return fmt.Errorf("credential data is empty")
	}

	// Provider-specific validation
	switch cred.Provider {
	case ProviderAzure:
		required := []string{"clientId", "clientSecret", "tenantId", "subscriptionId"}
		for _, key := range required {
			if val, exists := cred.Data[key]; !exists || strings.TrimSpace(val) == "" {
				return fmt.Errorf("missing required Azure credential: %s", key)
			}
		}

	case ProviderDigitalOcean, ProviderCivo:
		if val, exists := cred.Data["token"]; !exists || strings.TrimSpace(val) == "" {
			return fmt.Errorf("missing required %s token", cred.Provider)
		}

	case ProviderAWS:
		required := []string{"accessKeyId", "secretAccessKey"}
		for _, key := range required {
			if val, exists := cred.Data[key]; !exists || strings.TrimSpace(val) == "" {
				return fmt.Errorf("missing required AWS credential: %s", key)
			}
		}

	case ProviderGCP:
		if val, exists := cred.Data["credentials"]; !exists || strings.TrimSpace(val) == "" {
			return fmt.Errorf("missing required GCP credentials")
		}
	}

	return nil
}

// parseAzureCredentials parses Azure credentials from JSON
func parseAzureCredentials(data []byte) (map[string]string, error) {
	// Simple JSON parsing - in production, use json.Unmarshal
	result := make(map[string]string)
	// TODO: Implement proper JSON parsing
	return result, fmt.Errorf("not implemented")
}

// parseGCPCredentials parses GCP credentials
func parseGCPCredentials(data []byte) (map[string]string, error) {
	result := map[string]string{
		"credentials": string(data),
	}
	return result, nil
}

// parseAWSCredentials parses AWS credentials from INI format
func parseAWSCredentials(data []byte) (map[string]string, error) {
	result := make(map[string]string)
	// TODO: Implement proper INI parsing
	return result, fmt.Errorf("not implemented")
}
