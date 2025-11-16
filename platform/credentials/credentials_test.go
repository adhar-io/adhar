package credentials

import (
	"context"
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func TestCredentialManager_DiscoverFromEnvironment(t *testing.T) {
	tests := []struct {
		name     string
		provider Provider
		envVars  map[string]string
		want     bool
	}{
		{
			name:     "Azure with all credentials",
			provider: ProviderAzure,
			envVars: map[string]string{
				"AZURE_CLIENT_ID":       "test-client-id",
				"AZURE_CLIENT_SECRET":   "test-secret",
				"AZURE_TENANT_ID":       "test-tenant",
				"AZURE_SUBSCRIPTION_ID": "test-subscription",
			},
			want: true,
		},
		{
			name:     "DigitalOcean with token",
			provider: ProviderDigitalOcean,
			envVars: map[string]string{
				"DIGITALOCEAN_TOKEN": "test-token",
			},
			want: true,
		},
		{
			name:     "Civo with token",
			provider: ProviderCivo,
			envVars: map[string]string{
				"CIVO_TOKEN": "test-token",
			},
			want: true,
		},
		{
			name:     "Azure with missing credentials",
			provider: ProviderAzure,
			envVars: map[string]string{
				"AZURE_CLIENT_ID": "test-client-id",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variables
			for key, value := range tt.envVars {
				os.Setenv(key, value)
				defer os.Unsetenv(key)
			}

			cm := &CredentialManager{}
			got := cm.discoverFromEnvironment(tt.provider)

			if (got != nil) != tt.want {
				t.Errorf("discoverFromEnvironment() got = %v, want %v", got != nil, tt.want)
			}

			if got != nil && got.Source != SourceEnvironment {
				t.Errorf("discoverFromEnvironment() source = %v, want %v", got.Source, SourceEnvironment)
			}
		})
	}
}

func TestCredentialManager_DiscoverFromSecrets(t *testing.T) {
	tests := []struct {
		name     string
		provider Provider
		secrets  []*corev1.Secret
		want     bool
	}{
		{
			name:     "Azure credentials in secret",
			provider: ProviderAzure,
			secrets: []*corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "azure-credentials",
						Namespace: "crossplane-system",
					},
					Data: map[string][]byte{
						"AZURE_CLIENT_ID":       []byte("test-client-id"),
						"AZURE_CLIENT_SECRET":   []byte("test-secret"),
						"AZURE_TENANT_ID":       []byte("test-tenant"),
						"AZURE_SUBSCRIPTION_ID": []byte("test-subscription"),
					},
				},
			},
			want: true,
		},
		{
			name:     "DigitalOcean credentials in secret",
			provider: ProviderDigitalOcean,
			secrets: []*corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "digitalocean-credentials",
						Namespace: "crossplane-system",
					},
					Data: map[string][]byte{
						"DIGITALOCEAN_TOKEN": []byte("test-token"),
					},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewSimpleClientset()
			cm := &CredentialManager{
				k8sClient: fakeClient,
			}

			// Create secrets
			for _, secret := range tt.secrets {
				_, err := fakeClient.CoreV1().Secrets(secret.Namespace).Create(context.Background(), secret, metav1.CreateOptions{})
				if err != nil {
					t.Fatalf("Failed to create secret: %v", err)
				}
			}

			got, err := cm.discoverFromSecrets(context.Background(), tt.provider)

			if tt.want && err != nil {
				t.Errorf("discoverFromSecrets() error = %v, want nil", err)
			}

			if (got != nil) != tt.want {
				t.Errorf("discoverFromSecrets() got = %v, want %v", got != nil, tt.want)
			}

			if got != nil && got.Source != SourceSecret {
				t.Errorf("discoverFromSecrets() source = %v, want %v", got.Source, SourceSecret)
			}
		})
	}
}

func TestCredentialManager_ValidateCredentials(t *testing.T) {
	tests := []struct {
		name    string
		cred    *Credential
		wantErr bool
	}{
		{
			name: "Valid Azure credentials",
			cred: &Credential{
				Provider: ProviderAzure,
				Data: map[string]string{
					"clientId":       "test-client-id",
					"clientSecret":   "test-secret",
					"tenantId":       "test-tenant",
					"subscriptionId": "test-subscription",
				},
			},
			wantErr: false,
		},
		{
			name: "Invalid Azure credentials - missing field",
			cred: &Credential{
				Provider: ProviderAzure,
				Data: map[string]string{
					"clientId": "test-client-id",
				},
			},
			wantErr: true,
		},
		{
			name: "Valid DigitalOcean credentials",
			cred: &Credential{
				Provider: ProviderDigitalOcean,
				Data: map[string]string{
					"token": "test-token",
				},
			},
			wantErr: false,
		},
		{
			name:    "Nil credential",
			cred:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cm := &CredentialManager{}
			err := cm.ValidateCredentials(tt.cred)

			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCredentials() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNewCredentialManager(t *testing.T) {
	// Test with nil config (will fail in non-cluster environment)
	_, err := NewCredentialManager(nil)
	if err == nil {
		t.Skip("Skipping in-cluster config test - not running in cluster")
	}

	// Test with fake config
	config := &rest.Config{
		Host: "https://localhost:6443",
	}
	cm, err := NewCredentialManager(config)
	if err != nil {
		t.Errorf("NewCredentialManager() error = %v", err)
	}
	if cm == nil {
		t.Error("NewCredentialManager() returned nil manager")
	}
}
