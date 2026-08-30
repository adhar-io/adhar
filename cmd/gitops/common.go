package gitops

import (
	"encoding/base64"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Control-plane / GitOps backend resources the gitops subcommands act on. These
// are the exact same ArgoCD / Argo Workflows objects the Adhar Console drives, so
// `adhar gitops ...` and the Console operate on one backend.
var (
	// applicationsGVR is the ArgoCD Application (argoproj.io/v1alpha1) resource.
	applicationsGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	// gitopsWorkflowsGVR is the Argo Workflow (argoproj.io/v1alpha1) resource.
	gitopsWorkflowsGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "workflows"}
	// secretsGVR is the core v1 Secret resource (ArgoCD repository secrets).
	secretsGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
)

// argoNamespace is the namespace ArgoCD and its Applications/repo secrets live in.
const argoNamespace = "adhar-system"

// stringField returns a nested string value from an unstructured object map.
func stringField(obj map[string]interface{}, keys ...string) string {
	cur := obj
	for i, k := range keys {
		if i == len(keys)-1 {
			if v, ok := cur[k].(string); ok {
				return v
			}
			return ""
		}
		next, ok := cur[k].(map[string]interface{})
		if !ok {
			return ""
		}
		cur = next
	}
	return ""
}

// secretDataString reads a base64-encoded value from a Secret's .data map (as
// returned by the dynamic client) and decodes it to a plain string.
func secretDataString(obj map[string]interface{}, key string) string {
	raw := stringField(obj, "data", key)
	if raw == "" {
		return ""
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return raw
	}
	return string(decoded)
}

// valueOrDash renders an empty string as a dash for tabular output.
func valueOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
