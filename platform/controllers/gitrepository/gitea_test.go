package gitrepository

import (
	"context"
	"testing"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/platform/utils"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGiteaAdminSecretObject(t *testing.T) {
	// Gitea runs in the platform namespace (adhar-system), not its own.
	secret := utils.GiteaAdminSecretObject()
	assert.Equal(t, "adhar-system", secret.Namespace)
	assert.Equal(t, "gitea-credential", secret.Name)
	assert.Equal(t, "Secret", secret.Kind)
}

func TestPatchPasswordSecret(t *testing.T) {
	ctx := context.TODO()
	kubeClient := fake.NewClientBuilder().Build()
	config := v1alpha1.BuildCustomizationSpec{}

	// Create a mock secret
	mockSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"password": []byte("old-password"),
		},
	}
	kubeClient.Create(ctx, mockSecret)

	err := utils.PatchPasswordSecret(ctx, kubeClient, config, "default", "test-secret", "admin", "new-password")
	assert.NoError(t, err)

	updatedSecret := &corev1.Secret{}
	err = kubeClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: "test-secret"}, updatedSecret)
	assert.NoError(t, err)
	assert.Equal(t, "new-password", string(updatedSecret.Data["password"]))
}

func TestGetGiteaToken(t *testing.T) {
	ctx := context.TODO()

	// Mock behavior for token creation and deletion
	// This would require a mock library or interface implementation

	_, err := utils.GetGiteaToken(ctx, "http://mock-gitea", "admin", "password")
	assert.Error(t, err) // Replace with actual mock behavior validation
}

// The previous version of this test defined a local mock config, re-implemented
// the URL formatting inline, and asserted on its own output -- utils.GiteaBaseUrl
// was never called, so it could not fail and would not have caught either branch
// changing. It now exercises the real routing rule.
func TestGiteaBaseUrl(t *testing.T) {
	tests := []struct {
		name string
		cfg  v1alpha1.BuildCustomizationSpec
		want string
	}{
		{
			name: "subdomain routing gives gitea its own host",
			cfg: v1alpha1.BuildCustomizationSpec{
				Protocol: "http", Host: "localhost", Port: "3000", UsePathRouting: false,
			},
			want: "http://gitea.localhost:3000",
		},
		{
			name: "path routing keeps one host and adds a prefix",
			cfg: v1alpha1.BuildCustomizationSpec{
				Protocol: "http", Host: "localhost", Port: "3000", UsePathRouting: true,
			},
			want: "http://localhost:3000/gitea",
		},
		{
			name: "https on the platform host",
			cfg: v1alpha1.BuildCustomizationSpec{
				Protocol: "https", Host: "adhar.localtest.me", Port: "8443", UsePathRouting: false,
			},
			want: "https://gitea.adhar.localtest.me:8443",
		},
		{
			name: "path routing on the platform host",
			cfg: v1alpha1.BuildCustomizationSpec{
				Protocol: "https", Host: "adhar.localtest.me", Port: "8443", UsePathRouting: true,
			},
			want: "https://adhar.localtest.me:8443/gitea",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, utils.GiteaBaseUrlFromConfig(tc.cfg))
		})
	}
}
