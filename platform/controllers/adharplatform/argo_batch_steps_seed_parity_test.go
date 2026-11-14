package adharplatform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/globals"
)

// TestBatchStepUpgradeDiffStagesTheImageThePushWrites: `adhar upgrade` compares
// Gitea with a staged stack and then pushes one, and the two used to be staged
// differently. The push goes through the reconciler, which renders
// {{ .PlatformImage }} as the manager Deployment's image; the diff went through
// the package-level StageStack, which has no cluster and renders the fallback.
// So adhar-batch-steps.yaml differed on EVERY upgrade, a no-op one included,
// and an upgrade that did change the image was invisible. The diff now stages
// through StageStackAsSeeded on the same reconciler as the push.
func TestBatchStepUpgradeDiffStagesTheImageThePushWrites(t *testing.T) {
	t.Setenv(PlatformImageEnv, "")
	const deployed = "ghcr.io/adhar-io/adhar:0.1.70"
	s := runtime.NewScheme()
	_ = appsv1.AddToScheme(s)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: managerDeployment, Namespace: globals.AdharSystemNamespace},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: managerContainer, Image: deployed},
		}}}},
	}
	r := &AdharPlatformReconciler{
		Client: fake.NewClientBuilder().WithScheme(s).WithObjects(dep).Build(),
		Config: argoStageData().BuildCustomizationSpec,
	}
	src := filepath.Join(stackPackagesDir(t), filepath.FromSlash(argoPkg))
	step := func(out string) string {
		b, err := os.ReadFile(filepath.Join(out, "manifests", "catalog", "adhar-batch-steps.yaml"))
		if err != nil {
			t.Fatalf("the staged package has no manifests/catalog/adhar-batch-steps.yaml: %v", err)
		}
		return string(b)
	}

	out, cleanup, err := r.StageStackAsSeeded(context.Background(), src)
	if err != nil {
		t.Fatalf("StageStackAsSeeded: %v", err)
	}
	defer cleanup()
	if got := step(out); !strings.Contains(got, "image: "+deployed) {
		t.Errorf("StageStackAsSeeded does not render the manager's image %s into the batch step; "+
			"the upgrade diff would disagree with what the push writes", deployed)
	}

	// Why the diff must not use the package-level function: without a cluster it
	// cannot know the image, so it renders something else.
	plain, cleanupPlain, err := StageStack(src, r.Config)
	if err != nil {
		t.Fatalf("StageStack: %v", err)
	}
	defer cleanupPlain()
	if strings.Contains(step(plain), "image: "+deployed) {
		t.Error("StageStack rendered the deployed image with no cluster to read it from; this guard no longer proves anything")
	}
}
