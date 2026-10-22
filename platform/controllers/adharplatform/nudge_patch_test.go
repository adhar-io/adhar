package adharplatform

import (
	"context"
	"testing"

	argov1alpha1 "github.com/cnoe-io/argocd-api/api/argo/application/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"adhar-io/adhar/globals"
)

// Argo CD rewrites an Application continuously while it is moving, and the
// CRD has no status subresource. The nudge must still land when the object
// changed between the list and the write — an Update of the listed copy lost
// that race on resourceVersion (and would have written stale status back had
// it won).
func TestTheNudgeLandsOnAnApplicationArgoCDIsRewriting(t *testing.T) {
	s := runtime.NewScheme()
	if err := argov1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	stuck := app(argov1alpha1.SyncStatusCodeUnknown, "Healthy")
	stuck.Name, stuck.Namespace = "keycloak", globals.AdharSystemNamespace

	var c client.Client
	c = fake.NewClientBuilder().WithScheme(s).WithObjects(stuck).WithInterceptorFuncs(interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if err := cl.List(ctx, list, opts...); err != nil {
				return err
			}
			// Argo CD's application controller writes the object right after we read it.
			live := &argov1alpha1.Application{}
			if err := cl.Get(ctx, client.ObjectKeyFromObject(stuck), live); err != nil {
				return err
			}
			live.Status.Health.Status = "Progressing"
			return cl.Update(ctx, live)
		},
	}).Build()

	r := &AdharPlatformReconciler{Client: c}
	report, err := r.nudgeApplications(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Refreshed != 1 {
		t.Fatalf("expected the stuck application to be refreshed, refreshed=%d", report.Refreshed)
	}
	got := &argov1alpha1.Application{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(stuck), got); err != nil {
		t.Fatal(err)
	}
	if got.Annotations[argoCDApplicationAnnotationKeyRefresh] == "" {
		t.Fatal("refresh annotation not set")
	}
	if got.Status.Health.Status != "Progressing" {
		t.Fatalf("the nudge overwrote Argo CD's newer status with the stale listed copy: %q", got.Status.Health.Status)
	}
}
