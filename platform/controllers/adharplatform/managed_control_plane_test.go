package adharplatform

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
)

func civoNode(name string, external string) *corev1.Node {
	addrs := []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "192.168.1." + name[len(name)-1:]}}
	if external != "" {
		addrs = append(addrs, corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: external})
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     corev1.NodeStatus{Addresses: addrs},
	}
}

func civoSpecCM(mode string) *corev1.ConfigMap {
	cm := clusterSpecCM(mode)
	cm.Data["provider"] = "civo"
	return cm
}

// On Civo's managed k3s the control plane runs on the node that carries the
// cluster's public address. Left untainted it fills with platform pods and the
// API server dies under the first sync wave (mum1, 2026-10-15). The Cilium
// stage — the first thing the controller does — must reserve it.
func TestTheCivoManagedControlPlaneNodeIsReservedBeforeAnythingIsScheduled(t *testing.T) {
	s := providedScheme(t)
	ctx := context.Background()
	platform := &v1alpha1.AdharPlatform{ObjectMeta: metav1.ObjectMeta{
		Name: "adhar", Namespace: globals.AdharSystemNamespace, UID: "uid",
	}}
	r := &AdharPlatformReconciler{
		Client: fake.NewClientBuilder().WithScheme(s).WithObjects(
			platform, foreignCiliumDaemonSet(), civoSpecCM("managed"),
			ciliumConfig(foreignCiliumNamespace, map[string]string{"kube-proxy-replacement": "true"}),
			takenOverBinding("cilium", "cilium"),
			civoNode("k3s-adhar-node-1", "212.2.252.7"),
			civoNode("k3s-adhar-node-2", ""),
			civoNode("k3s-adhar-node-3", ""),
		).Build(),
		Scheme: s,
	}
	if _, err := r.ReconcileCilium(ctx, ctrlRequestFor(platform), platform); err != nil {
		t.Fatalf("ReconcileCilium: %v", err)
	}

	master := &corev1.Node{}
	if err := r.Get(ctx, client.ObjectKey{Name: "k3s-adhar-node-1"}, master); err != nil {
		t.Fatal(err)
	}
	if !hasControlPlaneTaint(master) {
		t.Error("the node hosting the k3s server (the one with the public address) was not tainted NoSchedule; " +
			"platform pods will share it with the control plane")
	}
	if _, ok := master.Labels[controlPlaneRoleLabel]; !ok {
		t.Error("the control-plane node carries no control-plane role label")
	}
	for _, worker := range []string{"k3s-adhar-node-2", "k3s-adhar-node-3"} {
		n := &corev1.Node{}
		if err := r.Get(ctx, client.ObjectKey{Name: worker}, n); err != nil {
			t.Fatal(err)
		}
		if hasControlPlaneTaint(n) {
			t.Errorf("worker %s was tainted; only the node with the public address hosts the control plane", worker)
		}
	}

	// Idempotent: a second pass adds no second taint.
	if _, err := r.ReconcileCilium(ctx, ctrlRequestFor(platform), platform); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKey{Name: "k3s-adhar-node-1"}, master); err != nil {
		t.Fatal(err)
	}
	if n := len(master.Spec.Taints); n != 1 {
		t.Errorf("after two passes the control-plane node carries %d taints, want 1", n)
	}
}

// Hosted control planes have no special node; a non-Civo managed cluster and a
// cluster with no single public node must be left alone.
func TestOnlyCivoManagedClustersGetAControlPlaneNodeReserved(t *testing.T) {
	s := providedScheme(t)
	ctx := context.Background()
	platform := &v1alpha1.AdharPlatform{ObjectMeta: metav1.ObjectMeta{
		Name: "adhar", Namespace: globals.AdharSystemNamespace, UID: "uid",
	}}
	doks := clusterSpecCM("managed")
	doks.Data["provider"] = "digitalocean"
	r := &AdharPlatformReconciler{
		Client: fake.NewClientBuilder().WithScheme(s).WithObjects(
			platform, foreignCiliumDaemonSet(), doks,
			ciliumConfig(foreignCiliumNamespace, map[string]string{"kube-proxy-replacement": "true"}),
			takenOverBinding("cilium", "cilium"),
			civoNode("pool-1", "1.2.3.4"),
		).Build(),
		Scheme: s,
	}
	if _, err := r.ReconcileCilium(ctx, ctrlRequestFor(platform), platform); err != nil {
		t.Fatal(err)
	}
	n := &corev1.Node{}
	if err := r.Get(ctx, client.ObjectKey{Name: "pool-1"}, n); err != nil {
		t.Fatal(err)
	}
	if hasControlPlaneTaint(n) {
		t.Error("a DOKS node was tainted; its control plane is hosted by the cloud")
	}

	// Two public nodes: ambiguous, so nothing is chosen.
	if got := managedControlPlaneNode([]corev1.Node{*civoNode("a1", "1.1.1.1"), *civoNode("b2", "2.2.2.2")}); got != nil {
		t.Errorf("with two public nodes %s was picked; guessing taints a worker", got.Name)
	}
}

// Civo's cloud-controller assigns the public address AFTER the cluster reports
// ready, so a reservation that runs once during the Cilium step finds no node.
// It must run every reconcile until the node is known.
func TestTheControlPlaneNodeIsReservedOnceItsPublicAddressAppears(t *testing.T) {
	s := providedScheme(t)
	ctx := context.Background()
	r := &AdharPlatformReconciler{
		Client: fake.NewClientBuilder().WithScheme(s).WithObjects(
			civoSpecCM("managed"), civoNode("k3s-adhar-node-1", ""), civoNode("k3s-adhar-node-2", ""),
		).Build(),
		Scheme: s,
	}
	if _, err := r.reserveManagedControlPlaneNode(ctx); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"k3s-adhar-node-1", "k3s-adhar-node-2"} {
		n := &corev1.Node{}
		if err := r.Get(ctx, client.ObjectKey{Name: name}, n); err != nil {
			t.Fatal(err)
		}
		if hasControlPlaneTaint(n) {
			t.Fatalf("%s was tainted before any node had a public address", name)
		}
	}
	// The cloud-controller catches up.
	late := &corev1.Node{}
	if err := r.Get(ctx, client.ObjectKey{Name: "k3s-adhar-node-1"}, late); err != nil {
		t.Fatal(err)
	}
	late.Status.Addresses = append(late.Status.Addresses, corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: "212.2.254.162"})
	if err := r.Status().Update(ctx, late); err != nil {
		t.Fatal(err)
	}
	if _, err := r.reserveManagedControlPlaneNode(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKey{Name: "k3s-adhar-node-1"}, late); err != nil {
		t.Fatal(err)
	}
	if !hasControlPlaneTaint(late) {
		t.Fatal("the node was not reserved on the pass after its public address appeared")
	}

	b, err := os.ReadFile("controller.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	call := strings.Index(src, "r.reserveManagedControlPlaneNode(ctx)\n")
	foundation := strings.Index(src, "// Install FOUNDATION packages only")
	if call < 0 || foundation < 0 || call > foundation {
		t.Error("Reconcile does not reserve the managed control-plane node on every pass before the foundation steps; a late public address leaves the k3s server unprotected")
	}
}

// A NoSchedule taint does not move pods that already landed, so a reservation
// that arrives after the foundation is installed protects nothing. On Civo
// (2026-10-20) Argo CD, Gitea, Crossplane and Falco were scheduled onto the
// k3s server's node two minutes before its public address appeared, and the
// API server died four minutes later. Reconcile must WAIT for the node.
func TestNothingIsInstalledBeforeTheControlPlaneNodeIsReserved(t *testing.T) {
	s := providedScheme(t)
	ctx := context.Background()
	platform := &v1alpha1.AdharPlatform{ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "adhar-system"}}
	r := &AdharPlatformReconciler{
		Client: fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(platform).WithObjects(
			platform, civoSpecCM("managed"), civoNode("k3s-adhar-node-1", ""), civoNode("k3s-adhar-node-2", ""),
		).Build(),
		Scheme: s,
	}
	pending, err := r.reserveManagedControlPlaneNode(ctx)
	if err != nil || !pending {
		t.Fatalf("no node has its public address yet: want pending, got pending=%t err=%v", pending, err)
	}
	res, err := r.Reconcile(ctx, ctrlRequestFor(platform))
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter == 0 || res.RequeueAfter > 30*time.Second || r.reserveWaitStart.IsZero() {
		t.Fatalf("Reconcile did not wait for the control-plane node (result %+v)", res)
	}
	// It must have stopped BEFORE the foundation: on this fake cluster any
	// install attempt fails and is recorded as the last failure.
	if r.lastFailureMessage != "" {
		t.Fatalf("Reconcile tried to install the platform while the control-plane node was unreserved: %s", r.lastFailureMessage)
	}
	// Once the wait is spent it proceeds rather than blocking forever.
	r.reserveWaitStart = time.Now().Add(-controlPlaneReserveWait - time.Second)
	if p, _ := r.reserveManagedControlPlaneNode(ctx); !p {
		t.Fatal("still unidentified, so still pending")
	}

	// A cluster whose control plane is hosted never waits.
	doks := clusterSpecCM("managed")
	doks.Data["provider"] = "digitalocean"
	r2 := &AdharPlatformReconciler{Client: fake.NewClientBuilder().WithScheme(s).WithObjects(doks, civoNode("n", "")).Build(), Scheme: s}
	if p, _ := r2.reserveManagedControlPlaneNode(ctx); p {
		t.Error("a DOKS cluster was held waiting for a control-plane node it does not have")
	}
}

// On Civo's managed k3s the API server is reached through the control-plane
// node's Cilium. Restarting that agent to pick up Gateway API preceded the API
// going dark on two runs in a row (2026-10-20), so on such a cluster only the
// OTHER nodes' agents are restarted, by pod, and the DaemonSet template — whose
// change would roll every node — is left alone.
func TestCiliumIsRestartedEverywhereButTheManagedControlPlaneNode(t *testing.T) {
	s := providedScheme(t)
	ctx := context.Background()
	sel := map[string]string{"k8s-app": "cilium"}
	ds := foreignCiliumDaemonSet()
	ds.Spec.Selector = &metav1.LabelSelector{MatchLabels: sel}
	master := civoNode("k3s-node-1", "212.2.252.137")
	master.Spec.Taints = []corev1.Taint{{Key: controlPlaneTaintKey, Effect: corev1.TaintEffectNoSchedule}}
	pod := func(name, node string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: foreignCiliumNamespace, Labels: sel},
			Spec: corev1.PodSpec{NodeName: node}}
	}
	r := &AdharPlatformReconciler{
		Client: fake.NewClientBuilder().WithScheme(s).WithObjects(
			civoSpecCM("managed"), ds, master, civoNode("k3s-node-2", ""), civoNode("k3s-node-3", ""),
			pod("cilium-a", "k3s-node-1"), pod("cilium-b", "k3s-node-2"), pod("cilium-c", "k3s-node-3"),
		).Build(),
		Scheme: s,
	}
	if err := r.restartCilium(ctx, foreignCiliumNamespace); err != nil {
		t.Fatal(err)
	}
	got := &appsv1.DaemonSet{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(ds), got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Template.Annotations[restartedAtAnnotation] != "" {
		t.Error("the Cilium DaemonSet template was changed, which rolls the agent on the control-plane node too")
	}
	for name, want := range map[string]bool{"cilium-a": true, "cilium-b": false, "cilium-c": false} {
		err := r.Get(ctx, client.ObjectKey{Namespace: foreignCiliumNamespace, Name: name}, &corev1.Pod{})
		if exists := err == nil; exists != want {
			t.Errorf("pod %s: exists=%t, want %t (only the control-plane node's agent is spared)", name, exists, want)
		}
	}
}
