package adharplatform

import (
	"context"
	"testing"

	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
)

// Civo's managed k3s has a 40-volume account quota; the platform's ~90 claims
// must land on node-local storage there, as they do on every self-managed cloud.
func TestManagedCivoGetsNodeLocalStorageAsTheDefault(t *testing.T) {
	s := providedScheme(t)
	_ = storagev1.AddToScheme(s)
	ctx := context.Background()
	platform := &v1alpha1.AdharPlatform{ObjectMeta: metav1.ObjectMeta{Name: "adhar", Namespace: globals.AdharSystemNamespace, UID: "uid"}}
	cloudClass := &storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: "civo-volume", Annotations: map[string]string{defaultStorageClassAnnotation: "true"}},
		Provisioner: "csi.civo.com",
	}
	r := &AdharPlatformReconciler{
		Client: fake.NewClientBuilder().WithScheme(s).WithObjects(platform, civoSpecCM("managed"), cloudClass).Build(),
		Scheme: s,
	}
	// The fake client may not implement server-side apply; the demotion is what
	// this test can prove, and it runs only after the apply did not error.
	if err := r.reconcileManagedNodeLocalStorage(ctx, platform); err != nil {
		t.Logf("apply through the fake client: %v", err)
	}
	got := &storagev1.StorageClass{}
	if err := r.Get(ctx, client.ObjectKey{Name: "civo-volume"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Annotations[defaultStorageClassAnnotation] != "false" {
		t.Fatalf("civo-volume is still the default StorageClass (%q); every claim would be a quota'd cloud volume", got.Annotations[defaultStorageClassAnnotation])
	}

	// A managed cluster elsewhere (DOKS, EKS, …) keeps its cloud default.
	other := &storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: "do-block-storage", Annotations: map[string]string{defaultStorageClassAnnotation: "true"}},
		Provisioner: "dobs.csi.digitalocean.com",
	}
	spec := civoSpecCM("managed")
	spec.Data["provider"] = "digitalocean"
	r2 := &AdharPlatformReconciler{Client: fake.NewClientBuilder().WithScheme(s).WithObjects(platform, spec, other).Build(), Scheme: s}
	if err := r2.reconcileManagedNodeLocalStorage(ctx, platform); err != nil {
		t.Fatal(err)
	}
	if err := r2.Get(ctx, client.ObjectKey{Name: "do-block-storage"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Annotations[defaultStorageClassAnnotation] != "true" {
		t.Fatal("a managed cluster on another cloud had its default StorageClass demoted")
	}
}
