package policy

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"adhar-io/adhar/platform/k8s"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
)

// fieldManager identifies the CLI as the owner of server-side-applied fields.
const fieldManager = "adhar-cli"

// kubeClients builds a dynamic client plus a discovery-backed RESTMapper so
// arbitrary manifests (Kyverno ClusterPolicy/Policy and friends) can be resolved
// to their GroupVersionResource and scope, then applied via the dynamic client.
func kubeClients() (dynamic.Interface, meta.RESTMapper, error) {
	config, err := k8s.GetKubeConfig()
	if err != nil {
		return nil, nil, err
	}
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, nil, err
	}
	dc, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, nil, err
	}
	groups, err := restmapper.GetAPIGroupResources(dc)
	if err != nil {
		return nil, nil, err
	}
	return dyn, restmapper.NewDiscoveryRESTMapper(groups), nil
}

// decodeManifests splits a (possibly multi-document) YAML/JSON stream into
// unstructured objects, skipping empty documents.
func decodeManifests(data []byte) ([]*unstructured.Unstructured, error) {
	var objs []*unstructured.Unstructured
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	for {
		raw := map[string]interface{}{}
		if err := dec.Decode(&raw); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("parse manifest: %w", err)
		}
		if len(raw) == 0 {
			continue
		}
		u := &unstructured.Unstructured{Object: raw}
		if u.GetKind() == "" || u.GetAPIVersion() == "" {
			return nil, fmt.Errorf("manifest document missing apiVersion/kind")
		}
		objs = append(objs, u)
	}
	return objs, nil
}

// resourceInterface resolves an object's GVK to a dynamic ResourceInterface,
// honouring cluster vs namespace scope. namespaceOverride is applied to
// namespaced objects that don't already carry a namespace.
func resourceInterface(dyn dynamic.Interface, mapper meta.RESTMapper, obj *unstructured.Unstructured, namespaceOverride string) (dynamic.ResourceInterface, error) {
	gvk := obj.GroupVersionKind()
	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w (is the CRD installed?)", gvk.Kind, err)
	}
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		ns := obj.GetNamespace()
		if ns == "" {
			ns = namespaceOverride
		}
		if ns == "" {
			ns = "default"
		}
		obj.SetNamespace(ns)
		return dyn.Resource(mapping.Resource).Namespace(ns), nil
	}
	return dyn.Resource(mapping.Resource), nil
}

// applyManifest server-side-applies a single object (create-or-update, idempotent).
// When dry is true the apply is validated by the API server but not persisted.
func applyManifest(ctx context.Context, dyn dynamic.Interface, mapper meta.RESTMapper, obj *unstructured.Unstructured, namespaceOverride string, dry bool) error {
	ri, err := resourceInterface(dyn, mapper, obj, namespaceOverride)
	if err != nil {
		return err
	}
	data, err := obj.MarshalJSON()
	if err != nil {
		return err
	}
	force := true
	opts := metav1.PatchOptions{FieldManager: fieldManager, Force: &force}
	if dry {
		opts.DryRun = []string{metav1.DryRunAll}
	}
	_, err = ri.Patch(ctx, obj.GetName(), types.ApplyPatchType, data, opts)
	return err
}
