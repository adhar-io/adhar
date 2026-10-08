package adharplatform

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
)

// The live incident this guards, in one sentence: Civo's managed k3s runs Cilium
// (because the Civo provider asks for it), the platform installed a SECOND
// Cilium into adhar-system, Server-Side Apply took over the cluster-scoped
// ClusterRoleBinding `cilium` so the cluster's own agent lost its permissions,
// and a cluster with no working CNI hung `adhar up` at the Argo CD stage with
// every pod Pending (2026-10-07).
//
// Installing a second Cilium does not give you two Ciliums. It gives you none.

const foreignCiliumNamespace = "kube-system"

func foreignCiliumDaemonSet() *appsv1.DaemonSet {
	return &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{
		Name: "cilium", Namespace: foreignCiliumNamespace,
	}}
}

// platformCiliumDaemonSet is what a previous release installed on top of it.
func platformCiliumDaemonSet(name string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: globals.AdharSystemNamespace,
	}}
}

// takenOverBinding is the ClusterRoleBinding as the incident left it: one
// subject, the platform's own ServiceAccount, and the cluster's own locked out.
func takenOverBinding(name, serviceAccount string) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: name},
		Subjects: []rbacv1.Subject{{
			Kind: rbacv1.ServiceAccountKind, Name: serviceAccount,
			Namespace: globals.AdharSystemNamespace,
		}},
	}
}

func ciliumConfig(namespace string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: ciliumConfigMapName, Namespace: namespace},
		Data:       data,
	}
}

// A cluster that already runs Cilium must keep it — in MANAGED mode too, which
// is the mode the outage happened in. The previous version of this check only
// ran for `provided`.
func TestReconcileCiliumNeverInstallsOverTheClustersOwn(t *testing.T) {
	for _, mode := range []string{"managed", "compute", "provided"} {
		t.Run(mode, func(t *testing.T) {
			s := providedScheme(t)
			platform := &v1alpha1.AdharPlatform{ObjectMeta: metav1.ObjectMeta{
				Name: "adhar", Namespace: globals.AdharSystemNamespace, UID: "uid",
			}}
			r := &AdharPlatformReconciler{
				Client: fake.NewClientBuilder().WithScheme(s).WithObjects(
					platform, foreignCiliumDaemonSet(), clusterSpecCM(mode),
					ciliumConfig(foreignCiliumNamespace, map[string]string{"kube-proxy-replacement": "true"}),
					takenOverBinding("cilium", "cilium"),
					takenOverBinding("cilium-operator", "cilium-operator"),
				).Build(),
				Scheme: s,
			}

			if _, err := r.ReconcileCilium(context.Background(), ctrlRequestFor(platform), platform); err != nil {
				t.Fatalf("ReconcileCilium: %v", err)
			}

			list := &appsv1.DaemonSetList{}
			if err := r.List(context.Background(), list); err != nil {
				t.Fatalf("listing DaemonSets: %v", err)
			}
			for i := range list.Items {
				if list.Items[i].Namespace == globals.AdharSystemNamespace {
					t.Errorf("the platform installed %s/%s on a cluster that already runs Cilium — "+
						"the two installs share cluster-scoped names and the result is NO working CNI",
						list.Items[i].Namespace, list.Items[i].Name)
				}
			}
		})
	}
}

// On a cluster that is OURS, adopting the cluster's Cilium means: give its
// ServiceAccounts their permissions back, turn on the one feature the platform
// needs, and remove the platform's own install that can never become ready.
func TestAdoptingTheClustersCiliumRepairsAndConfiguresIt(t *testing.T) {
	s := providedScheme(t)
	ctx := context.Background()
	r := &AdharPlatformReconciler{
		Client: fake.NewClientBuilder().WithScheme(s).WithObjects(
			foreignCiliumDaemonSet(),
			&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "cilium-envoy", Namespace: foreignCiliumNamespace}},
			&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "cilium-operator", Namespace: foreignCiliumNamespace}},
			// Civo's managed k3s ships cilium-config with NO Gateway API key.
			ciliumConfig(foreignCiliumNamespace, map[string]string{"kube-proxy-replacement": "true"}),
			takenOverBinding("cilium", "cilium"),
			takenOverBinding("cilium-operator", "cilium-operator"),
			platformCiliumDaemonSet("cilium"),
			platformCiliumDaemonSet("cilium-envoy"),
			&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "cilium-operator", Namespace: globals.AdharSystemNamespace}},
			&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "hubble-relay", Namespace: globals.AdharSystemNamespace}},
		).Build(),
		Scheme: s,
	}

	if err := r.adoptForeignCilium(ctx, foreignCiliumNamespace); err != nil {
		t.Fatalf("adoptForeignCilium: %v", err)
	}

	// 1. The cluster's own ServiceAccounts are bound again. This is the one that
	//    took the cluster down: `namespaces "kube-system" is forbidden`.
	for name, sa := range map[string]string{"cilium": "cilium", "cilium-operator": "cilium-operator"} {
		binding := &rbacv1.ClusterRoleBinding{}
		if err := r.Get(ctx, client.ObjectKey{Name: name}, binding); err != nil {
			t.Fatalf("reading ClusterRoleBinding %s: %v", name, err)
		}
		found := false
		for _, s := range binding.Subjects {
			if s.Namespace == foreignCiliumNamespace && s.Name == sa {
				found = true
			}
		}
		if !found {
			t.Errorf("ClusterRoleBinding %s does not bind %s/%s; the cluster's Cilium stays unauthorized and crash-looping",
				name, foreignCiliumNamespace, sa)
		}
		// The repair ADDS; it must not evict whoever else is on the binding.
		if len(binding.Subjects) < 2 {
			t.Errorf("ClusterRoleBinding %s has %d subject(s): the repair rewrote the list instead of adding to it",
				name, len(binding.Subjects))
		}
	}

	// 2. Gateway API is on, or the platform Gateway is never Programmed and not
	//    one platform URL answers.
	cm := &corev1.ConfigMap{}
	if err := r.Get(ctx, client.ObjectKey{Name: ciliumConfigMapName, Namespace: foreignCiliumNamespace}, cm); err != nil {
		t.Fatalf("reading cilium-config: %v", err)
	}
	for k, want := range ciliumGatewayAPIConfig {
		if cm.Data[k] != want {
			t.Errorf("cilium-config[%s] = %q, want %q", k, cm.Data[k], want)
		}
	}
	if cm.Data["kube-proxy-replacement"] != "true" {
		t.Error("the cluster's own Cilium settings were lost; only the Gateway API keys may change")
	}

	// 3. The agents and operator were restarted, because neither the config nor
	//    the permissions take effect until they are.
	ds := &appsv1.DaemonSet{}
	if err := r.Get(ctx, types.NamespacedName{Name: "cilium", Namespace: foreignCiliumNamespace}, ds); err != nil {
		t.Fatalf("reading the cluster's cilium DaemonSet: %v", err)
	}
	if ds.Spec.Template.Annotations[restartedAtAnnotation] == "" {
		t.Error("the cluster's Cilium agents were not restarted, so the new configuration never takes effect")
	}

	// 4. The platform's own Cilium is gone. It can never become ready — the
	//    cluster's install holds the host ports — and a second operator competes
	//    for the same cluster-scoped state.
	for _, name := range platformCiliumWorkloads.daemonSets {
		leftover := &appsv1.DaemonSet{}
		err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: globals.AdharSystemNamespace}, leftover)
		if err == nil {
			t.Errorf("the platform's own DaemonSet %s is still there; its pods stay Pending forever", name)
		}
	}
	leftover := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Name: "hubble-relay", Namespace: globals.AdharSystemNamespace}, leftover); err == nil {
		t.Error("the platform's hubble-relay is still there; it cannot reach a Hubble the cluster may not run")
	}

	// And the cluster's OWN workloads must survive. Deleting those is how the
	// incident started.
	if err := r.Get(ctx, types.NamespacedName{Name: "cilium", Namespace: foreignCiliumNamespace}, ds); err != nil {
		t.Errorf("the cluster's own Cilium DaemonSet was deleted: %v", err)
	}
}

// Idempotence matters more than usual here: this runs on every reconcile, and a
// pass that restarts the cluster's CNI each time is worse than the bug.
func TestAdoptingTheClustersCiliumIsIdempotent(t *testing.T) {
	s := providedScheme(t)
	ctx := context.Background()
	r := &AdharPlatformReconciler{
		Client: fake.NewClientBuilder().WithScheme(s).WithObjects(
			foreignCiliumDaemonSet(),
			ciliumConfig(foreignCiliumNamespace, nil),
			takenOverBinding("cilium", "cilium"),
			takenOverBinding("cilium-operator", "cilium-operator"),
		).Build(),
		Scheme: s,
	}
	if err := r.adoptForeignCilium(ctx, foreignCiliumNamespace); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	ds := &appsv1.DaemonSet{}
	key := types.NamespacedName{Name: "cilium", Namespace: foreignCiliumNamespace}
	if err := r.Get(ctx, key, ds); err != nil {
		t.Fatalf("reading the DaemonSet: %v", err)
	}
	first := ds.Spec.Template.Annotations[restartedAtAnnotation]

	if err := r.adoptForeignCilium(ctx, foreignCiliumNamespace); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if err := r.Get(ctx, key, ds); err != nil {
		t.Fatalf("re-reading the DaemonSet: %v", err)
	}
	if ds.Spec.Template.Annotations[restartedAtAnnotation] != first {
		t.Error("the second pass restarted the cluster's Cilium again; nothing had changed")
	}

	binding := &rbacv1.ClusterRoleBinding{}
	if err := r.Get(ctx, client.ObjectKey{Name: "cilium"}, binding); err != nil {
		t.Fatalf("reading the binding: %v", err)
	}
	if len(binding.Subjects) != 2 {
		t.Errorf("the binding has %d subjects after two passes; the repair is appending on every reconcile",
			len(binding.Subjects))
	}
}

// A PROVIDED cluster's Cilium is the operator's. Nothing is repaired,
// reconfigured or restarted — the preflight refuses a cluster whose Cilium has
// Gateway API disabled instead, because reconfiguring someone else's CNI is not
// a side effect installing a platform gets to have.
func TestAProvidedClustersCiliumIsNeverTouched(t *testing.T) {
	s := providedScheme(t)
	ctx := context.Background()
	platform := &v1alpha1.AdharPlatform{ObjectMeta: metav1.ObjectMeta{
		Name: "adhar", Namespace: globals.AdharSystemNamespace, UID: "uid",
	}}
	r := &AdharPlatformReconciler{
		Client: fake.NewClientBuilder().WithScheme(s).WithObjects(
			platform, foreignCiliumDaemonSet(), clusterSpecCM("provided"),
			ciliumConfig(foreignCiliumNamespace, map[string]string{"kube-proxy-replacement": "true"}),
			takenOverBinding("cilium", "cilium"),
		).Build(),
		Scheme: s,
	}

	if _, err := r.ReconcileCilium(ctx, ctrlRequestFor(platform), platform); err != nil {
		t.Fatalf("ReconcileCilium: %v", err)
	}

	cm := &corev1.ConfigMap{}
	if err := r.Get(ctx, client.ObjectKey{Name: ciliumConfigMapName, Namespace: foreignCiliumNamespace}, cm); err != nil {
		t.Fatalf("reading cilium-config: %v", err)
	}
	if _, ok := cm.Data["enable-gateway-api"]; ok {
		t.Error("a provided cluster's Cilium configuration was changed; its Cilium belongs to the operator")
	}
	binding := &rbacv1.ClusterRoleBinding{}
	if err := r.Get(ctx, client.ObjectKey{Name: "cilium"}, binding); err != nil {
		t.Fatalf("reading the binding: %v", err)
	}
	if len(binding.Subjects) != 1 {
		t.Error("a provided cluster's Cilium RBAC was changed")
	}
	ds := &appsv1.DaemonSet{}
	if err := r.Get(ctx, types.NamespacedName{Name: "cilium", Namespace: foreignCiliumNamespace}, ds); err != nil {
		t.Fatalf("reading the DaemonSet: %v", err)
	}
	if ds.Spec.Template.Annotations[restartedAtAnnotation] != "" {
		t.Error("a provided cluster's Cilium was restarted")
	}
}
