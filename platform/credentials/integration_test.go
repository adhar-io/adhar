//go:build integration
// +build integration

package credentials

import (
	"context"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// TestIntegration_CredentialDiscovery tests the full credential discovery flow
// against a real Kubernetes cluster. Run with: go test -tags=integration
func TestIntegration_CredentialDiscovery(t *testing.T) {
	// Skip if not in integration test mode
	if os.Getenv("RUN_INTEGRATION_TESTS") != "true" {
		t.Skip("Skipping integration test. Set RUN_INTEGRATION_TESTS=true to run")
	}

	ctx := context.Background()

	// Load kubeconfig
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = os.ExpandEnv("$HOME/.kube/config")
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("Failed to build config: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatalf("Failed to create clientset: %v", err)
	}

	// Create test namespace
	testNamespace := "credential-test-" + time.Now().Format("20060102150405")
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: testNamespace,
			Labels: map[string]string{
				"test": "credential-discovery",
			},
		},
	}

	_, err = clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create test namespace: %v", err)
	}

	// Cleanup namespace after test
	defer func() {
		err := clientset.CoreV1().Namespaces().Delete(ctx, testNamespace, metav1.DeleteOptions{})
		if err != nil {
			t.Logf("Warning: Failed to cleanup test namespace: %v", err)
		}
	}()

	t.Run("DiscoverAndCreateAWSCredentials", func(t *testing.T) {
		// Create AWS credentials secret
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "aws-credentials",
				Namespace: testNamespace,
			},
			StringData: map[string]string{
				"AWS_ACCESS_KEY_ID":     "AKIAIOSFODNN7EXAMPLE",
				"AWS_SECRET_ACCESS_KEY": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			},
		}

		_, err := clientset.CoreV1().Secrets(testNamespace).Create(ctx, secret, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("Failed to create test secret: %v", err)
		}

		// Create credential manager
		cm := &CredentialManager{
			k8sClient: clientset,
		}

		// Discover credentials
		cred, err := cm.discoverFromSecrets(ctx, ProviderAWS)
		if err != nil {
			t.Fatalf("Failed to discover credentials: %v", err)
		}

		if cred == nil {
			t.Fatal("Expected to discover credentials, got nil")
		}

		// Validate discovered credentials
		if err := cm.ValidateCredentials(cred); err != nil {
			t.Fatalf("Credential validation failed: %v", err)
		}

		// Test credential data
		if cred.Data["accessKeyId"] != "AKIAIOSFODNN7EXAMPLE" {
			t.Errorf("Expected accessKeyId to be AKIAIOSFODNN7EXAMPLE, got %s", cred.Data["accessKeyId"])
		}

		// Test secret creation
		secretRef, err := cm.GetOrCreateSecret(ctx, cred, testNamespace, "test-aws-creds")
		if err != nil {
			t.Fatalf("Failed to create secret: %v", err)
		}

		if secretRef.Name != "test-aws-creds" {
			t.Errorf("Expected secret name to be test-aws-creds, got %s", secretRef.Name)
		}

		// Verify secret was created
		createdSecret, err := clientset.CoreV1().Secrets(testNamespace).Get(ctx, "test-aws-creds", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("Failed to get created secret: %v", err)
		}

		if string(createdSecret.Data["accessKeyId"]) != "AKIAIOSFODNN7EXAMPLE" {
			t.Error("Created secret does not contain expected data")
		}
	})

	t.Run("DiscoverAzureCredentials", func(t *testing.T) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "azure-credentials",
				Namespace: testNamespace,
			},
			StringData: map[string]string{
				"AZURE_CLIENT_ID":       "12345678-1234-1234-1234-123456789012",
				"AZURE_CLIENT_SECRET":   "test-secret",
				"AZURE_TENANT_ID":       "87654321-4321-4321-4321-210987654321",
				"AZURE_SUBSCRIPTION_ID": "11111111-1111-1111-1111-111111111111",
			},
		}

		_, err := clientset.CoreV1().Secrets(testNamespace).Create(ctx, secret, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("Failed to create test secret: %v", err)
		}

		cm := &CredentialManager{
			k8sClient: clientset,
		}

		cred, err := cm.discoverFromSecrets(ctx, ProviderAzure)
		if err != nil {
			t.Fatalf("Failed to discover Azure credentials: %v", err)
		}

		if cred == nil {
			t.Fatal("Expected to discover Azure credentials, got nil")
		}

		if err := cm.ValidateCredentials(cred); err != nil {
			t.Fatalf("Azure credential validation failed: %v", err)
		}
	})

	t.Run("DiscoverDigitalOceanCredentials", func(t *testing.T) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "digitalocean-credentials",
				Namespace: testNamespace,
			},
			StringData: map[string]string{
				"DIGITALOCEAN_TOKEN": "dop_v1_test_token_1234567890abcdef",
			},
		}

		_, err := clientset.CoreV1().Secrets(testNamespace).Create(ctx, secret, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("Failed to create test secret: %v", err)
		}

		cm := &CredentialManager{
			k8sClient: clientset,
		}

		cred, err := cm.discoverFromSecrets(ctx, ProviderDigitalOcean)
		if err != nil {
			t.Fatalf("Failed to discover DigitalOcean credentials: %v", err)
		}

		if cred == nil {
			t.Fatal("Expected to discover DigitalOcean credentials, got nil")
		}

		if err := cm.ValidateCredentials(cred); err != nil {
			t.Fatalf("DigitalOcean credential validation failed: %v", err)
		}

		if cred.Data["token"] != "dop_v1_test_token_1234567890abcdef" {
			t.Error("Token does not match expected value")
		}
	})

	t.Run("DiscoverCivoCredentials", func(t *testing.T) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "civo-credentials",
				Namespace: testNamespace,
			},
			StringData: map[string]string{
				"CIVO_TOKEN": "civo_test_token_1234567890",
			},
		}

		_, err := clientset.CoreV1().Secrets(testNamespace).Create(ctx, secret, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("Failed to create test secret: %v", err)
		}

		cm := &CredentialManager{
			k8sClient: clientset,
		}

		cred, err := cm.discoverFromSecrets(ctx, ProviderCivo)
		if err != nil {
			t.Fatalf("Failed to discover Civo credentials: %v", err)
		}

		if cred == nil {
			t.Fatal("Expected to discover Civo credentials, got nil")
		}

		if err := cm.ValidateCredentials(cred); err != nil {
			t.Fatalf("Civo credential validation failed: %v", err)
		}
	})

	t.Run("HandleMissingCredentials", func(t *testing.T) {
		cm := &CredentialManager{
			k8sClient: clientset,
		}

		// Try to discover credentials for a provider without secrets
		_, err := cm.discoverFromSecrets(ctx, ProviderGCP)
		if err == nil {
			t.Error("Expected error when discovering missing credentials, got nil")
		}
	})

	t.Run("ValidateInvalidCredentials", func(t *testing.T) {
		cm := &CredentialManager{
			k8sClient: clientset,
		}

		// Test with incomplete credentials
		cred := &Credential{
			Provider: ProviderAWS,
			Data: map[string]string{
				"accessKeyId": "AKIAIOSFODNN7EXAMPLE",
				// Missing secretAccessKey
			},
		}

		err := cm.ValidateCredentials(cred)
		if err == nil {
			t.Error("Expected validation error for incomplete credentials, got nil")
		}
	})

	t.Run("SecretUpdate", func(t *testing.T) {
		cm := &CredentialManager{
			k8sClient: clientset,
		}

		// Create initial credential
		cred := &Credential{
			Provider: ProviderAWS,
			Source:   SourceEnvironment,
			Data: map[string]string{
				"accessKeyId":     "AKIAIOSFODNN7EXAMPLE",
				"secretAccessKey": "initial-secret",
			},
		}

		// Create secret
		secretRef, err := cm.GetOrCreateSecret(ctx, cred, testNamespace, "update-test")
		if err != nil {
			t.Fatalf("Failed to create initial secret: %v", err)
		}

		// Update credential data
		cred.Data["secretAccessKey"] = "updated-secret"

		// Update secret
		_, err = cm.GetOrCreateSecret(ctx, cred, testNamespace, "update-test")
		if err != nil {
			t.Fatalf("Failed to update secret: %v", err)
		}

		// Verify update
		updatedSecret, err := clientset.CoreV1().Secrets(testNamespace).Get(ctx, secretRef.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("Failed to get updated secret: %v", err)
		}

		if string(updatedSecret.Data["secretAccessKey"]) != "updated-secret" {
			t.Error("Secret was not updated with new data")
		}
	})
}

// TestIntegration_MultipleProviders tests discovering credentials for multiple providers
func TestIntegration_MultipleProviders(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") != "true" {
		t.Skip("Skipping integration test. Set RUN_INTEGRATION_TESTS=true to run")
	}

	ctx := context.Background()

	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = os.ExpandEnv("$HOME/.kube/config")
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("Failed to build config: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatalf("Failed to create clientset: %v", err)
	}

	testNamespace := "credential-multi-test-" + time.Now().Format("20060102150405")
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: testNamespace,
		},
	}

	_, err = clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create test namespace: %v", err)
	}

	defer func() {
		clientset.CoreV1().Namespaces().Delete(ctx, testNamespace, metav1.DeleteOptions{})
	}()

	// Create secrets for multiple providers
	providers := []struct {
		name     string
		provider Provider
		data     map[string]string
	}{
		{
			name:     "aws-credentials",
			provider: ProviderAWS,
			data: map[string]string{
				"AWS_ACCESS_KEY_ID":     "AKIAIOSFODNN7EXAMPLE",
				"AWS_SECRET_ACCESS_KEY": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			},
		},
		{
			name:     "digitalocean-credentials",
			provider: ProviderDigitalOcean,
			data: map[string]string{
				"DIGITALOCEAN_TOKEN": "dop_v1_test_token",
			},
		},
		{
			name:     "civo-credentials",
			provider: ProviderCivo,
			data: map[string]string{
				"CIVO_TOKEN": "civo_test_token",
			},
		},
	}

	cm := &CredentialManager{
		k8sClient: clientset,
	}

	for _, p := range providers {
		t.Run(string(p.provider), func(t *testing.T) {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      p.name,
					Namespace: testNamespace,
				},
				StringData: p.data,
			}

			_, err := clientset.CoreV1().Secrets(testNamespace).Create(ctx, secret, metav1.CreateOptions{})
			if err != nil {
				t.Fatalf("Failed to create %s secret: %v", p.provider, err)
			}

			cred, err := cm.discoverFromSecrets(ctx, p.provider)
			if err != nil {
				t.Fatalf("Failed to discover %s credentials: %v", p.provider, err)
			}

			if cred == nil {
				t.Fatalf("Expected to discover %s credentials, got nil", p.provider)
			}

			if err := cm.ValidateCredentials(cred); err != nil {
				t.Fatalf("%s credential validation failed: %v", p.provider, err)
			}
		})
	}
}
