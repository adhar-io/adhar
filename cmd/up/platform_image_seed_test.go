package up

// A fresh `adhar up` must seed the catalogue's workflow steps with the image it
// installs as the manager (docs/design/0026 D-19, 0025 D10, §12.1).
//
// The reconciler finds that image on the manager Deployment, but only the CLI's
// in-process manager seeds the GitOps repos, and on a fresh cluster it does so
// BEFORE EnsureControllerManager creates that Deployment. Every fresh cluster
// therefore shipped adhar-batch-steps on ghcr.io/adhar-io/adhar:latest (the
// fallback), with imagePullPolicy IfNotPresent, while the manager beside it ran
// the release (found by the wfstep verifier, 2026-11-14: live manager 0.1.69).
// The CLI now decides the image first and hands it over through
// ADHAR_PLATFORM_IMAGE (seedPlatformImage) on both the cloud and the local path.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/platform/controllers/adharplatform"
)

const batchStepsFile = "packages/application/argo-workflows/manifests/catalog/adhar-batch-steps.yaml"

// clearPlatformImageEnv unsets ADHAR_PLATFORM_IMAGE for one test and restores
// it afterwards (t.Setenv alone would leave it SET to "", which seedPlatformImage
// treats the same, but os.Setenv inside the test must not leak either way).
func clearPlatformImageEnv(t *testing.T) {
	t.Helper()
	t.Setenv(adharplatform.PlatformImageEnv, "")
	_ = os.Unsetenv(adharplatform.PlatformImageEnv)
}

// TestAFreshClusterSeedsTheStepWithTheInstalledImage drives the reconciler the
// in-process manager runs, against a cluster with no manager Deployment yet,
// after seedPlatformImage: the staged step must name the image being installed.
func TestAFreshClusterSeedsTheStepWithTheInstalledImage(t *testing.T) {
	clearPlatformImageEnv(t)
	const installed = "ghcr.io/adhar-io/adhar:0.1.69"
	seedPlatformImage(installed)

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	r := &adharplatform.AdharPlatformReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).Build(), // no adhar-controller-manager yet
		Config: v1alpha1.BuildCustomizationSpec{Host: "adhar.localtest.me", Port: "8443"},
	}
	staged, cleanup, err := r.StageStackAsSeeded(context.Background(), filepath.Join("..", "..", "platform", "stack"))
	if err != nil {
		t.Fatalf("staging the stack: %v", err)
	}
	defer cleanup()
	raw, err := os.ReadFile(filepath.Join(staged, batchStepsFile))
	if err != nil {
		t.Fatalf("the staged stack has no %s: %v", batchStepsFile, err)
	}
	if !strings.Contains(string(raw), "image: "+installed+"\n") {
		t.Errorf("a fresh cluster seeds adhar-batch-steps without the installed manager image %s; "+
			"image lines:\n%s", installed, imageLines(string(raw)))
	}
}

// TestAnOperatorPlatformImageIsKept: ADHAR_PLATFORM_IMAGE exported by the
// operator is a choice, not something `adhar up` overwrites.
func TestAnOperatorPlatformImageIsKept(t *testing.T) {
	t.Setenv(adharplatform.PlatformImageEnv, "registry.example/adhar:pinned")
	seedPlatformImage("ghcr.io/adhar-io/adhar:0.1.69")
	if got := os.Getenv(adharplatform.PlatformImageEnv); got != "registry.example/adhar:pinned" {
		t.Errorf("ADHAR_PLATFORM_IMAGE = %q; the operator's value was overwritten", got)
	}
	clearPlatformImageEnv(t)
	seedPlatformImage("  ")
	if _, set := os.LookupEnv(adharplatform.PlatformImageEnv); set {
		t.Error("an empty image was exported; the reconciler's own resolution must decide then")
	}
}

// TestTheLocalPathSeedsTheImageItInstalls: one rule for the Deployment and the
// seed, called before the in-process manager starts.
func TestTheLocalPathSeedsTheImageItInstalls(t *testing.T) {
	if got := (&LocalProvisioner{options: &LocalOptions{ControllerImage: "x/adhar:1"}}).controllerImage(); got != "x/adhar:1" {
		t.Errorf("controllerImage() = %q, want the --controller-image value", got)
	}
	if got := (&LocalProvisioner{options: &LocalOptions{}}).controllerImage(); got != defaultControllerImage() {
		t.Errorf("controllerImage() = %q, want this build's release %q", got, defaultControllerImage())
	}

	src := readSource(t, "local.go")
	run := funcBody(t, src, "func (b *LocalProvisioner) RunControllers(")
	seed, start := strings.Index(run, "seedPlatformImage(b.controllerImage())"), strings.Index(run, "controllers.RunControllers(")
	if seed < 0 || start < 0 || seed > start {
		t.Error("LocalProvisioner.RunControllers must call seedPlatformImage(b.controllerImage()) before " +
			"controllers.RunControllers: the in-process manager seeds the step before the Deployment exists")
	}
	install := funcBody(t, src, "func installInClusterController(")
	if !strings.Contains(install, "Image:        b.controllerImage(),") {
		t.Error("installInClusterController must install b.controllerImage(), the image the stack was seeded with")
	}
}

// TestTheCloudPathSeedsTheImageItInstalls: the cloud bootstrap resolves the
// manager image once, seeds it, then installs that same variable.
func TestTheCloudPathSeedsTheImageItInstalls(t *testing.T) {
	src := funcBody(t, readSource(t, "bootstrap.go"), "func bootstrapPlatformOnCluster(")
	resolve := strings.Index(src, "image := helpers.ResolveControllerImage(")
	seed := strings.Index(src, "seedPlatformImage(image)")
	start := strings.Index(src, "controllers.RunControllers(")
	install := strings.Index(src, "controllers.EnsureControllerManager(")
	if resolve < 0 || seed < 0 || start < 0 || install < 0 || !(resolve < seed && seed < start && start < install) {
		t.Fatalf("bootstrapPlatformOnCluster must resolve the manager image, seedPlatformImage(image), start the "+
			"controllers and then install that image, in that order (resolve=%d seed=%d start=%d install=%d)",
			resolve, seed, start, install)
	}
	between := src[seed:install]
	if strings.Contains(between, "image :=") || strings.Contains(between, "image =") {
		t.Error("the manager image is re-decided after the stack was seeded; the step and the manager would differ")
	}
	if !strings.Contains(src[install:], "Image:        image,") {
		t.Error("EnsureControllerManager must install `image`, the image the stack was seeded with")
	}
}

func readSource(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(raw)
}

// funcBody is the source from a top-level func's signature to the next one.
func funcBody(t *testing.T, src, signature string) string {
	t.Helper()
	i := strings.Index(src, signature)
	if i < 0 {
		t.Fatalf("%s not found", signature)
	}
	rest := src[i+len(signature):]
	if j := strings.Index(rest, "\nfunc "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func imageLines(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, "image:") {
			out = append(out, strings.TrimSpace(l))
		}
	}
	return strings.Join(out, "\n")
}
