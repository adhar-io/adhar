package controllers

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"io"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/platform/utils/fs"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

//go:embed resources/manager/*.yaml
var managerFS embed.FS

// ManagerConfig parameterizes the embedded controller-manager manifests.
type ManagerConfig struct {
	// Image is the adhar container image to run (entrypoint `adhar controller`).
	Image string
	// Namespace the manager Deployment and ServiceAccount are created in.
	Namespace string
	// PlatformName is the AdharPlatform resource the manager reconciles, and it
	// MUST be passed. The manager reads the platform host from that resource; when
	// the name does not match it logs "AdharPlatform resource not found yet" and
	// carries on with an EMPTY build configuration. Every manifest that builds a
	// URL from the host then renders nonsense — the Argo CD SSO proxy became
	// "https://keycloak./realms/adhar" and crash-looped — and because the flag
	// defaulted to "adhar", this happened on every platform named anything else.
	PlatformName string
}

// EnsureControllerManager applies the controller-manager Deployment and RBAC so
// platform reconciliation continues in-cluster after the CLI process exits. It
// is idempotent: manifests are applied with server-side apply and force
// ownership, matching how the platform applies all other resources.
func EnsureControllerManager(ctx context.Context, kubeClient client.Client, cfg ManagerConfig) error {
	if cfg.Image == "" {
		return fmt.Errorf("controller manager image must not be empty")
	}
	if cfg.Namespace == "" {
		return fmt.Errorf("controller manager namespace must not be empty")
	}
	if cfg.PlatformName == "" {
		return fmt.Errorf("controller manager platform name must not be empty: without it the manager cannot read the platform host and renders host-derived manifests with an empty hostname")
	}

	rawDocs, err := fs.ConvertFSToBytes(managerFS, "resources/manager", cfg)
	if err != nil {
		return fmt.Errorf("rendering controller manager manifests: %w", err)
	}

	for _, doc := range rawDocs {
		decoder := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(doc), 4096)
		for {
			obj := &unstructured.Unstructured{}
			decodeErr := decoder.Decode(obj)
			if decodeErr == io.EOF {
				break
			}
			if decodeErr != nil {
				return fmt.Errorf("decoding controller manager manifest: %w", decodeErr)
			}
			if obj.Object == nil {
				continue
			}
			if err := kubeClient.Patch(ctx, obj, client.Apply, client.FieldOwner(v1alpha1.FieldManager), client.ForceOwnership); err != nil {
				return fmt.Errorf("applying %s %s: %w", obj.GetKind(), obj.GetName(), err)
			}
		}
	}
	return nil
}
