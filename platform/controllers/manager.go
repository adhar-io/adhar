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
