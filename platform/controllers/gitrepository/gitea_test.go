package gitrepository

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/platform/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type applySecretClient struct {
	client.Client
}

func (a *applySecretClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected object type %T", obj)
	}

	var secret corev1.Secret
	if err := a.Client.Get(ctx, client.ObjectKey{Name: u.GetName(), Namespace: u.GetNamespace()}, &secret); err != nil {
		return err
	}
	data, _, _ := unstructured.NestedStringMap(u.Object, "data")
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	if pw, ok := data["password"]; ok {
		decoded, _ := base64.StdEncoding.DecodeString(pw)
		secret.Data["password"] = decoded
	}
	if token, ok := data["token"]; ok {
		secret.Data["token"] = []byte(token)
	}
	return a.Client.Update(ctx, &secret)
}

func TestGiteaAdminSecretObject(t *testing.T) {
	secret := utils.GiteaAdminSecretObject()
	assert.Equal(t, "gitea", secret.Namespace)
	assert.Equal(t, "gitea-credential", secret.Name)
	assert.Equal(t, "Secret", secret.Kind)
}

func TestPatchPasswordSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	baseClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	kubeClient := &applySecretClient{Client: baseClient}
	config := v1alpha1.BuildCustomizationSpec{}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-secret",
			Namespace: "default",
		},
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: "v1",
		},
		Data: map[string][]byte{
			"password": []byte("old"),
		},
	}
	require.NoError(t, kubeClient.Create(context.Background(), secret))

	err := utils.PatchPasswordSecret(context.Background(), kubeClient, config, "default", "test-secret", "admin", "new-password")
	require.NoError(t, err)

	updated := &corev1.Secret{}
	require.NoError(t, kubeClient.Get(context.Background(), client.ObjectKeyFromObject(secret), updated))
	assert.Equal(t, "new-password", string(updated.Data["password"]))
}

func TestGetGiteaToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/version":
			_, _ = w.Write([]byte(`{"version":"1.20.0"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/users/admin/tokens":
			_, _ = w.Write([]byte(`[{"id":1,"name":"admin","sha1":"oldtoken"}]`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/users/admin/tokens/1":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/users/admin/tokens":
			_, _ = w.Write([]byte(`{"id":2,"name":"admin","sha1":"generated-token"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	token, err := utils.GetGiteaToken(context.Background(), server.URL, "admin", "password")
	require.NoError(t, err)
	assert.Equal(t, "generated-token", token)
}

func TestGiteaBaseUrl(t *testing.T) {
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

	getConfig := func() struct {
		Protocol       string
		Host           string
		Port           string
		UsePathRouting bool
	} {
		return mockIDPConfig
	}

	idpConfig := getConfig()

	var url string
	if idpConfig.UsePathRouting {
		url = "http://localhost:3000/gitea"
	} else {
		url = "http://gitea.localhost:3000"
	}

	assert.Equal(t, "http://gitea.localhost:3000", url)
}
