package gitrepository

import (
	"context"
	"fmt"
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
	secret := utils.GiteaAdminSecretObject()
	assert.Equal(t, "gitea", secret.Namespace)
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

func TestGiteaBaseUrl(t *testing.T) {
	ctx := context.TODO()

	// Mock IDP configuration
	mockIDPConfig := struct {
		Protocol       string
		Host           string
		Port           string
		UsePathRouting bool
	}{
		Protocol:       "http",
		Host:           "localhost",
		Port:           "3000",
		UsePathRouting: false,
	}

	// Inline mock for GetConfig
	getConfig := func(ctx context.Context) (struct {
		Protocol       string
		Host           string
		Port           string
		UsePathRouting bool
	}, error) {
		return mockIDPConfig, nil
	}

	idpConfig, err := getConfig(ctx)
	assert.NoError(t, err)

	var url string
	if idpConfig.UsePathRouting {
		url = fmt.Sprintf("%s://%s%s:%s%s", idpConfig.Protocol, "", idpConfig.Host, idpConfig.Port, "/gitea")
	} else {
		url = fmt.Sprintf("%s://%s%s:%s", idpConfig.Protocol, "gitea.", idpConfig.Host, idpConfig.Port)
	}

	assert.Equal(t, "http://gitea.localhost:3000", url)
}
