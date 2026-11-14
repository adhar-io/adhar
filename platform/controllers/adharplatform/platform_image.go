package adharplatform

import (
	"context"
	"os"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"adhar-io/adhar/globals"
)

// The platform image is the adhar container image itself, run as a workflow
// step (docs/design/0026 D-19, §5.7; 0025 §12.1). Catalogue templates name it
// as `{{ .PlatformImage }}`, rendered when the stack is staged, so a step's
// code and the control plane that seeded it are always one release: a step
// image that drifted from the manager would run CLI logic the platform's
// policies and compositions were never tested against.
//
// Where it comes from, first answer wins:
//  1. AdharPlatformReconciler.PlatformImage — the caller that started the
//     manager knows what it installed (`adhar up --controller-image`).
//  2. ADHAR_PLATFORM_IMAGE in the process environment. `adhar up` sets it
//     to the image it is about to install as the manager, because the
//     in-process manager that seeds a fresh cluster runs BEFORE that
//     Deployment exists, and (3) would otherwise find nothing.
//  3. The image the in-cluster manager Deployment runs right now, which is
//     what an in-cluster reconcile and every later re-seed see.
//  4. fallbackPlatformImage, the development build's image.
//
// A template that renders with no image at all would fail the whole seed
// (Go templates error on a missing method), so (4) is never empty.

const (
	// PlatformImageEnv overrides the platform image for this process.
	PlatformImageEnv = "ADHAR_PLATFORM_IMAGE"
	// fallbackPlatformImage matches cmd/helpers.FallbackControllerImage, the
	// image a build with no published release runs.
	fallbackPlatformImage = "ghcr.io/adhar-io/adhar:latest"
	// managerDeployment and managerContainer name the in-cluster manager
	// (platform/controllers/resources/manager/manager.yaml).
	managerDeployment = "adhar-controller-manager"
	managerContainer  = "manager"
)

// PlatformImage is the image catalogue steps run (`{{ .PlatformImage }}`).
func (d stackTemplateData) PlatformImage() string {
	if d.platformImage != "" {
		return d.platformImage
	}
	if v := strings.TrimSpace(os.Getenv(PlatformImageEnv)); v != "" {
		return v
	}
	return fallbackPlatformImage
}

// resolvePlatformImage is the image the reconciler stages the stack with; ""
// leaves the choice to PlatformImage's environment and fallback.
func (r *AdharPlatformReconciler) resolvePlatformImage(ctx context.Context) string {
	if v := strings.TrimSpace(r.PlatformImage); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv(PlatformImageEnv)); v != "" {
		return v
	}
	if r.Client == nil {
		return ""
	}
	return managerImage(ctx, r.Client)
}

// StageStackAsSeeded renders srcDir exactly as this reconciler's seed would,
// the platform image included, for a caller that compares the result with
// Gitea (`adhar upgrade`'s diff). The package-level StageStack cannot: it has
// no cluster to ask which image the manager runs, so a catalogue step rendered
// by it names the fallback image while the push writes the manager's, and
// every upgrade reported that file as changed whether or not it was.
func (r *AdharPlatformReconciler) StageStackAsSeeded(ctx context.Context, srcDir string) (string, func(), error) {
	return r.stageStack(ctx, srcDir)
}

// managerImage reads the manager container's image from its Deployment; ""
// when there is none (the CLI's in-process bootstrap, before it is installed).
func managerImage(ctx context.Context, c client.Reader) string {
	var dep appsv1.Deployment
	if err := c.Get(ctx, client.ObjectKey{Namespace: globals.AdharSystemNamespace, Name: managerDeployment}, &dep); err != nil {
		return ""
	}
	for _, ct := range dep.Spec.Template.Spec.Containers {
		if ct.Name == managerContainer {
			return strings.TrimSpace(ct.Image)
		}
	}
	return ""
}
