package controllers

import (
	"bytes"
	"io"
	"testing"

	"adhar-io/adhar/platform/utils/fs"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
)

func TestManagerManifestsRender(t *testing.T) {
	cfg := ManagerConfig{
		Image:     "ghcr.io/adhar-io/adhar:0.1.0",
		Namespace: "adhar-system",
	}

	rawDocs, err := fs.ConvertFSToBytes(managerFS, "resources/manager", cfg)
	require.NoError(t, err)
	require.NotEmpty(t, rawDocs)

	objs := map[string]*unstructured.Unstructured{}
	for _, doc := range rawDocs {
		decoder := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(doc), 4096)
		for {
			obj := &unstructured.Unstructured{}
			err := decoder.Decode(obj)
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
			if obj.Object == nil {
				continue
			}
			objs[obj.GetKind()] = obj
		}
	}

	sa, ok := objs["ServiceAccount"]
	require.True(t, ok, "ServiceAccount must be present")
	assert.Equal(t, "adhar-controller-manager", sa.GetName())
	assert.Equal(t, "adhar-system", sa.GetNamespace())

	crb, ok := objs["ClusterRoleBinding"]
	require.True(t, ok, "ClusterRoleBinding must be present")
	subjects, found, err := unstructured.NestedSlice(crb.Object, "subjects")
	require.NoError(t, err)
	require.True(t, found)
	subject := subjects[0].(map[string]interface{})
	assert.Equal(t, "adhar-system", subject["namespace"])

	deploy, ok := objs["Deployment"]
	require.True(t, ok, "Deployment must be present")
	assert.Equal(t, "adhar-controller-manager", deploy.GetName())
	assert.Equal(t, "adhar-system", deploy.GetNamespace())

	containers, found, err := unstructured.NestedSlice(deploy.Object, "spec", "template", "spec", "containers")
	require.NoError(t, err)
	require.True(t, found)
	container := containers[0].(map[string]interface{})
	assert.Equal(t, "ghcr.io/adhar-io/adhar:0.1.0", container["image"])
	args, _ := container["args"].([]interface{})
	assert.Contains(t, args, "controller")

	// Service-link env injection is disabled per ADR-0011.
	enableServiceLinks, found, err := unstructured.NestedBool(deploy.Object, "spec", "template", "spec", "enableServiceLinks")
	require.NoError(t, err)
	require.True(t, found)
	assert.False(t, enableServiceLinks)
}

func TestEnsureControllerManagerValidation(t *testing.T) {
	err := EnsureControllerManager(t.Context(), nil, ManagerConfig{Namespace: "adhar-system"})
	assert.ErrorContains(t, err, "image")

	err = EnsureControllerManager(t.Context(), nil, ManagerConfig{Image: "img"})
	assert.ErrorContains(t, err, "namespace")
}
