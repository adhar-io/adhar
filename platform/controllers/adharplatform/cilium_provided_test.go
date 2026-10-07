package adharplatform

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
	provider "adhar-io/adhar/platform/providers"
)

func providedScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	return s
}

func clusterSpecCM(mode string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      globals.ClusterSpecConfigMapName,
			Namespace: globals.AdharSystemNamespace,
		},
		Data: map[string]string{
			globals.ClusterSpecClusterModeKey: mode,
			"provider":                        "custom",
			"clusterName":                     "adhar-prod",
		},
	}
}

// The cluster mode has to reach the in-cluster controller, and an absent or
// unreadable record must mean the mode the platform has always had — not the one
// that skips installing the CNI.
func TestClusterModeIsReadFromTheClusterSpecConfigMap(t *testing.T) {
	s := providedScheme(t)
	for _, tc := range []struct {
		name string
		objs []client.Object
		want string
	}{
		{"provided", []client.Object{clusterSpecCM("provided")}, provider.ClusterModeProvided},
		{"managed", []client.Object{clusterSpecCM("managed")}, provider.ClusterModeManaged},
		{"no ConfigMap at all", nil, provider.ClusterModeCompute},
		{"a mode nobody recognises", []client.Object{clusterSpecCM("eks")}, provider.ClusterModeCompute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &AdharPlatformReconciler{
				Client: fake.NewClientBuilder().WithScheme(s).WithObjects(tc.objs...).Build(),
				Scheme: s,
			}
			if got := r.clusterModeFromSpec(context.Background()); got != tc.want {
				t.Errorf("clusterModeFromSpec = %q, want %q", got, tc.want)
			}
		})
	}
}

// Whose Cilium is it? The platform's embedded manifest hardcodes `cilium` in
// the platform namespace, so a Cilium anywhere else is somebody else's. Getting
// this backwards is what makes the skip dangerous in both directions: too
// greedy and the platform never installs its own CNI, too narrow and it
// overwrites the operator's.
func TestForeignCiliumIsTheOneThisPlatformDidNotApply(t *testing.T) {
	s := providedScheme(t)

	theirs := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{
		Name:      "cilium",
		Namespace: "kube-system",
	}}
	ours := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{
		Name:      "cilium",
		Namespace: globals.AdharSystemNamespace,
	}}

	for _, tc := range []struct {
		name       string
		objs       []client.Object
		wantForeig bool
	}{
		{"somebody else's Cilium", []client.Object{theirs}, true},
		{"the platform's own Cilium", []client.Object{ours}, false},
		{"no Cilium at all", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &AdharPlatformReconciler{
				Client: fake.NewClientBuilder().WithScheme(s).WithObjects(tc.objs...).Build(),
				Scheme: s,
			}
			got, err := r.foreignCilium(context.Background())
			if err != nil {
				t.Fatalf("foreignCilium: %v", err)
			}
			if (got != nil) != tc.wantForeig {
				t.Errorf("foreignCilium found %v, want foreign=%v", got, tc.wantForeig)
			}
		})
	}
}

// The behaviour that matters: on a PROVIDED cluster that already runs somebody
// else's Cilium, the reconciler must not apply its own Cilium manifests over it.
// Doing so would replace that cluster's CNI version, IPAM mode and
// kubeProxyReplacement setting as a side effect of installing a platform, on
// infrastructure the operator never handed over.
func TestReconcileCiliumLeavesAProvidedClustersCiliumAlone(t *testing.T) {
	s := providedScheme(t)
	platform := &v1alpha1.AdharPlatform{ObjectMeta: metav1.ObjectMeta{
		Name: "adhar", Namespace: globals.AdharSystemNamespace, UID: "uid",
	}}
	theirCilium := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{
		Name:      "cilium",
		Namespace: "kube-system",
	}}

	r := &AdharPlatformReconciler{
		Client: fake.NewClientBuilder().WithScheme(s).
			WithObjects(platform, theirCilium, clusterSpecCM("provided")).Build(),
		Scheme: s,
	}

	if _, err := r.ReconcileCilium(context.Background(), ctrlRequestFor(platform), platform); err != nil {
		t.Fatalf("ReconcileCilium on a provided cluster: %v", err)
	}

	// Nothing of the platform's own Cilium may have been created. The embedded
	// install manifest puts Cilium in the platform namespace; if it had been
	// applied, that DaemonSet would exist.
	list := &appsv1.DaemonSetList{}
	if err := r.List(context.Background(), list); err != nil {
		t.Fatalf("listing DaemonSets: %v", err)
	}
	for i := range list.Items {
		if list.Items[i].Namespace == globals.AdharSystemNamespace {
			t.Errorf("the platform installed %s/%s onto a provided cluster that already runs Cilium",
				list.Items[i].Namespace, list.Items[i].Name)
		}
	}
}

func ctrlRequestFor(p *v1alpha1.AdharPlatform) ctrl.Request {
	return ctrl.Request{NamespacedName: k8stypes.NamespacedName{Name: p.Name, Namespace: p.Namespace}}
}
