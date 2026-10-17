package adharplatform

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/globals"
)

func syncedCopy(name, owner string, cert string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: globals.AdharSystemNamespace, Labels: map[string]string{
			secretSyncOwnerNameLabel: owner, secretSyncOwnerNamespaceLabel: globals.AdharSystemNamespace,
		}},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{corev1.TLSCertKey: []byte(cert), corev1.TLSPrivateKeyKey: []byte("key")},
	}
}

// Envoy serves Cilium's synced COPY of adhar-cert; a copy that no longer matches
// its source is deleted so the operator recreates it from the new certificate.
func TestAStaleCiliumSyncedCopyOfTheEdgeSecretIsRefreshed(t *testing.T) {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	source := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: globals.SelfSignedCertSecretName, Namespace: globals.AdharSystemNamespace},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{corev1.TLSCertKey: []byte("LETSENCRYPT"), corev1.TLSPrivateKeyKey: []byte("key")},
	}
	stale := syncedCopy("cilium-sync-secret-aaaa", globals.SelfSignedCertSecretName, "SELFSIGNED")
	fresh := syncedCopy("cilium-sync-secret-bbbb", "other-cert", "X")
	otherSource := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "other-cert", Namespace: globals.AdharSystemNamespace}, Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{corev1.TLSCertKey: []byte("X"), corev1.TLSPrivateKeyKey: []byte("key")}}
	orphan := syncedCopy("cilium-sync-secret-cccc", "gone-cert", "Y")
	r := &AdharPlatformReconciler{Client: fake.NewClientBuilder().WithScheme(s).WithObjects(source, stale, fresh, otherSource, orphan).Build(), Scheme: s}

	if err := r.reconcileGatewaySecretSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(stale), &corev1.Secret{}); !k8serrors.IsNotFound(err) {
		t.Fatalf("the stale synced copy survived (err=%v); Envoy keeps serving the old certificate", err)
	}
	for _, keep := range []*corev1.Secret{fresh, orphan} {
		if err := r.Get(context.Background(), client.ObjectKeyFromObject(keep), &corev1.Secret{}); err != nil {
			t.Fatalf("a copy that matches its source, or has none, was deleted: %s (%v)", keep.Name, err)
		}
	}
}
