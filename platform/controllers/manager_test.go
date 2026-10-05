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

// The manager hosts the node autoscaler, so it must be schedulable on a FULL
// cluster — otherwise the component that adds nodes is one of the Pending pods
// waiting for a node (2026-10-05: two workers at 100% requests, 32 Pending,
// manager among them). Critical priority so it preempts a workload; a
// control-plane toleration plus a preference for that node, which workloads
// cannot crowd.
func TestManagerIsSchedulableWhenTheClusterIsFull(t *testing.T) {
	rawDocs, err := fs.ConvertFSToBytes(managerFS, "resources/manager", ManagerConfig{
		Image: "ghcr.io/adhar-io/adhar:0.1.0", Namespace: "adhar-system", PlatformName: "adhar",
	})
	require.NoError(t, err)
	var deploy *unstructured.Unstructured
	for _, doc := range rawDocs {
		decoder := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(doc), 4096)
		for {
			obj := &unstructured.Unstructured{}
			if err := decoder.Decode(obj); err == io.EOF {
				break
			} else {
				require.NoError(t, err)
			}
			if obj.GetKind() == "Deployment" && obj.GetName() == "adhar-controller-manager" {
				deploy = obj
			}
		}
	}
	require.NotNil(t, deploy, "no adhar-controller-manager Deployment rendered")
	prio, _, _ := unstructured.NestedString(deploy.Object, "spec", "template", "spec", "priorityClassName")
	assert.Equal(t, "system-cluster-critical", prio, "the autoscaler's host must outrank every workload")
	tols, _, _ := unstructured.NestedSlice(deploy.Object, "spec", "template", "spec", "tolerations")
	tolerated := false
	for _, tl := range tols {
		m, _ := tl.(map[string]interface{})
		if m["key"] == "node-role.kubernetes.io/control-plane" {
			tolerated = true
		}
	}
	assert.True(t, tolerated, "the manager must tolerate the control-plane taint so it can run where workloads cannot")
	prefs, _, _ := unstructured.NestedSlice(deploy.Object, "spec", "template", "spec", "affinity", "nodeAffinity", "preferredDuringSchedulingIgnoredDuringExecution")
	assert.NotEmpty(t, prefs, "the manager must PREFER the control-plane node (a requirement would break single-node Kind)")
	req, _, _ := unstructured.NestedSlice(deploy.Object, "spec", "template", "spec", "affinity", "nodeAffinity", "requiredDuringSchedulingIgnoredDuringExecution", "nodeSelectorTerms")
	assert.Empty(t, req, "must not REQUIRE the control plane: Kind has one node and it is the control plane")
}
