package adharplatform

import (
	"context"
	"regexp"
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/globals"
)

func storageScheme(t *testing.T) *runtime.Scheme {
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := storagev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func class(name, provisioner string, isDefault bool) *storagev1.StorageClass {
	sc := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: name}, Provisioner: provisioner}
	if isDefault {
		sc.Annotations = map[string]string{defaultStorageClassAnnotation: "true"}
	}
	return sc
}

// The CI class copies the network block class where one exists, so a CI
// workspace follows its pod to a node with free CPU; elsewhere it copies the
// default class, and it is rebuilt once a block class appears and nothing uses it.
func TestTheCIStorageClassFollowsTheNetworkBlockClass(t *testing.T) {
	ctx := context.Background()
	s := storageScheme(t)
	r := &AdharPlatformReconciler{Client: fake.NewClientBuilder().WithScheme(s).WithObjects(
		class(globals.DefaultStorageClass, "rancher.io/local-path", true),
		class(globals.BlockStorageClass, "disk.csi.azure.com", false),
	).Build(), Scheme: s}
	if err := r.reconcileCIStorageClass(ctx); err != nil {
		t.Fatal(err)
	}
	got := &storagev1.StorageClass{}
	if err := r.Get(ctx, client.ObjectKey{Name: globals.CIStorageClass}, got); err != nil {
		t.Fatal(err)
	}
	if got.Provisioner != "disk.csi.azure.com" || *got.VolumeBindingMode != storagev1.VolumeBindingWaitForFirstConsumer {
		t.Fatalf("the CI class does not mirror the block class: %+v", got)
	}

	// No block class (Kind, Civo, bring-your-own): the default class is copied.
	local := &AdharPlatformReconciler{Client: fake.NewClientBuilder().WithScheme(s).WithObjects(
		class(globals.DefaultStorageClass, "rancher.io/local-path", true),
	).Build(), Scheme: s}
	if err := local.reconcileCIStorageClass(ctx); err != nil {
		t.Fatal(err)
	}
	if err := local.Get(ctx, client.ObjectKey{Name: globals.CIStorageClass}, got); err != nil || got.Provisioner != "rancher.io/local-path" {
		t.Fatalf("without a block class the CI class must copy the default one: %+v %v", got, err)
	}

	// A block class appears later: the unused CI class is rebuilt on it.
	if err := local.Create(ctx, class(globals.BlockStorageClass, "ebs.csi.aws.com", false)); err != nil {
		t.Fatal(err)
	}
	if err := local.reconcileCIStorageClass(ctx); err != nil {
		t.Fatal(err)
	}
	if err := local.Get(ctx, client.ObjectKey{Name: globals.CIStorageClass}, got); err != nil || got.Provisioner != "ebs.csi.aws.com" {
		t.Fatalf("the CI class was not rebuilt on the block class: %+v %v", got, err)
	}
}

// Every CI workspace claim asks for the CI class; a claim without one lands on
// node-local storage and pins its pipeline to one node.
func TestEveryCIWorkspaceUsesTheCIStorageClass(t *testing.T) {
	claim := regexp.MustCompile(`(?m)^( *)volumeClaimTemplate:\n\s*spec:\n\s*storageClassName: (\S+)`)
	any := regexp.MustCompile(`(?m)^ *volumeClaimTemplate:`)
	for _, f := range []string{
		"application/adhar-libraries/manifests/release.yaml",
		"application/adhar-libraries/manifests/triggers.yaml",
		"application/adhar-supply-chain/manifests/70-app-ci.yaml",
	} {
		body := readStackFile(t, f)
		want := len(any.FindAllString(body, -1))
		named := claim.FindAllStringSubmatch(body, -1)
		if want == 0 || len(named) != want {
			t.Errorf("%s: %d CI workspace claims, %d name a StorageClass", f, want, len(named))
		}
		for _, m := range named {
			if m[2] != globals.CIStorageClass {
				t.Errorf("%s: a CI workspace asks for %q, want %q", f, m[2], globals.CIStorageClass)
			}
		}
	}
}
