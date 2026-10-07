package adharplatform

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
)

func readyNode(name, internalIP string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: internalIP}},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
}

func notReadyNode(name, internalIP string) *corev1.Node {
	n := readyNode(name, internalIP)
	n.Status.Conditions[0].Status = corev1.ConditionFalse
	return n
}

func clusterSpecWithAddresses(addresses string) *corev1.ConfigMap {
	cm := clusterSpecCM("compute")
	cm.Data["provider"] = "civo"
	cm.Data[globals.ClusterSpecGatewayAddressesKey] = addresses
	return cm
}

// The host-network decision reaches the controller from the facts the CLI
// recorded. Nothing inside the cluster can work it out: a kubeadm cluster on
// Civo instances and one on EC2 are indistinguishable from in here, and only
// one of them will ever get a load-balancer address.
func TestGatewayHostNetworkIsDecidedFromTheRecordedCloudFacts(t *testing.T) {
	s := providedScheme(t)
	for _, tc := range []struct {
		name string
		cm   *corev1.ConfigMap
		want bool
	}{
		{"civo compute", clusterSpecWithAddresses("212.2.253.230"), true},
		{"civo managed", func() *corev1.ConfigMap {
			cm := clusterSpecCM("managed")
			cm.Data["provider"] = "civo"
			return cm
		}(), false},
		{"aws compute", func() *corev1.ConfigMap {
			cm := clusterSpecCM("compute")
			cm.Data["provider"] = "aws"
			return cm
		}(), false},
		{"no ConfigMap at all", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objs := []client.Object{}
			if tc.cm != nil {
				objs = append(objs, tc.cm)
			}
			r := &AdharPlatformReconciler{
				Client: fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build(),
				Scheme: s,
			}
			if got := r.gatewayHostNetworkRequired(context.Background()); got != tc.want {
				t.Errorf("gatewayHostNetworkRequired = %v, want %v", got, tc.want)
			}
		})
	}
}

// external-dns must be handed the PUBLIC addresses. Its only other candidates
// are the Gateway's status addresses, which in host-network mode are the nodes'
// private IPs — and a private address in a public zone, with
// --policy=upsert-only, is a record that resolves forever and connects never.
func TestGatewayPublicAddressesComeFromTheRecordedSpec(t *testing.T) {
	s := providedScheme(t)
	r := &AdharPlatformReconciler{
		Client: fake.NewClientBuilder().WithScheme(s).
			WithObjects(clusterSpecWithAddresses(" 212.2.253.230 , 74.220.17.9 ,")).Build(),
		Scheme: s,
	}
	got := r.gatewayPublicAddresses(context.Background())
	if len(got) != 2 || got[0] != "212.2.253.230" || got[1] != "74.220.17.9" {
		t.Errorf("gatewayPublicAddresses = %v, want the two recorded addresses, trimmed", got)
	}

	// And nothing invented when none were recorded: an empty list makes the
	// caller report that the hostnames have no record, which is the truth.
	r = &AdharPlatformReconciler{
		Client: fake.NewClientBuilder().WithScheme(s).WithObjects(clusterSpecCM("compute")).Build(),
		Scheme: s,
	}
	if got := r.gatewayPublicAddresses(context.Background()); len(got) != 0 {
		t.Errorf("gatewayPublicAddresses = %v on a cluster that recorded none", got)
	}
	if err := r.ensureGatewayExternalDNSTarget(context.Background(), nil); err == nil {
		t.Error("annotating the Gateway with no addresses succeeded; it must report that there is no record to publish")
	}
}

// The in-cluster endpoint. Pods resolve the platform hostnames too, and they
// must reach the host-network Envoy through the cluster — not through the
// public address, which is NAT'd back to a node and generally does not hairpin.
func TestGatewayShimPointsAtTheReadyNodesInternalAddresses(t *testing.T) {
	s := providedScheme(t)
	platform := &v1alpha1.AdharPlatform{ObjectMeta: metav1.ObjectMeta{
		Name: "adhar", Namespace: globals.AdharSystemNamespace, UID: "uid",
	}}
	r := &AdharPlatformReconciler{
		Client: fake.NewClientBuilder().WithScheme(s).WithObjects(
			platform,
			readyNode("worker-2", "192.168.1.5"),
			readyNode("master-1", "192.168.1.2"),
			notReadyNode("worker-3", "192.168.1.9"),
		).Build(),
		Scheme: s,
	}
	ctx := context.Background()
	if err := r.ensureGatewayHostNetworkShim(ctx, platform); err != nil {
		t.Fatalf("ensureGatewayHostNetworkShim: %v", err)
	}

	var svc corev1.Service
	key := types.NamespacedName{Name: gatewayHostNetworkServiceName, Namespace: globals.AdharSystemNamespace}
	if err := r.Get(ctx, key, &svc); err != nil {
		t.Fatalf("reading the shim Service: %v", err)
	}
	if len(svc.Spec.Selector) != 0 {
		t.Errorf("the shim Service has a selector %v; its endpoints are NODES, not pods", svc.Spec.Selector)
	}
	if len(svc.Spec.Ports) != 2 {
		t.Fatalf("the shim Service has %d ports, want 80 and 443", len(svc.Spec.Ports))
	}

	var slice discoveryv1.EndpointSlice
	if err := r.Get(ctx, key, &slice); err != nil {
		t.Fatalf("reading the shim EndpointSlice: %v", err)
	}
	if slice.Labels[discoveryv1.LabelServiceName] != gatewayHostNetworkServiceName {
		t.Error("the EndpointSlice is not bound to the Service by label, so the Service has no endpoints at all")
	}
	var addresses []string
	for _, ep := range slice.Endpoints {
		addresses = append(addresses, ep.Addresses...)
	}
	if strings.Join(addresses, ",") != "192.168.1.2,192.168.1.5" {
		t.Errorf("endpoints = %v, want the two READY nodes' internal addresses in order", addresses)
	}

	// Idempotent, and it tracks the cluster: a second pass after a node joins
	// must include it.
	if err := r.Create(ctx, readyNode("worker-4", "192.168.1.12")); err != nil {
		t.Fatalf("adding a node: %v", err)
	}
	if err := r.ensureGatewayHostNetworkShim(ctx, platform); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if err := r.Get(ctx, key, &slice); err != nil {
		t.Fatalf("re-reading the EndpointSlice: %v", err)
	}
	if len(slice.Endpoints) != 3 {
		t.Errorf("after a node joined the shim has %d endpoints, want 3", len(slice.Endpoints))
	}
}

// A cluster with no Ready node has no edge, and saying so is better than
// publishing a Service that answers with nothing.
func TestGatewayShimRefusesWhenNoNodeIsReady(t *testing.T) {
	s := providedScheme(t)
	platform := &v1alpha1.AdharPlatform{ObjectMeta: metav1.ObjectMeta{
		Name: "adhar", Namespace: globals.AdharSystemNamespace, UID: "uid",
	}}
	r := &AdharPlatformReconciler{
		Client: fake.NewClientBuilder().WithScheme(s).WithObjects(platform, notReadyNode("worker-1", "192.168.1.5")).Build(),
		Scheme: s,
	}
	if err := r.ensureGatewayHostNetworkShim(context.Background(), platform); err == nil {
		t.Error("the shim was created with no Ready node behind it")
	}
}

// CoreDNS has to point at the SHIM in host-network mode: there is no Gateway
// Service to point at, so the stock target would resolve to nothing and every
// oauth2-proxy would still fail OIDC discovery at start-up.
func TestCoreDNSPointsAtTheShimInHostNetworkMode(t *testing.T) {
	const stock = ".:53 {\n    errors\n    forward . /etc/resolv.conf\n}\n"

	hostNet, changed := insertGatewayRewrite(stock, "hub.adhar.io", gatewayHostNetworkServiceFQDN)
	if !changed {
		t.Fatal("the rewrite was not inserted")
	}
	if !strings.Contains(hostNet, gatewayHostNetworkServiceFQDN) {
		t.Error("the rewrite does not point at the host-network shim Service")
	}
	if strings.Contains(hostNet, gatewayServiceFQDN) {
		t.Error("the rewrite points at the Cilium Gateway Service, which host-network mode never creates")
	}

	svcMode, _ := insertGatewayRewrite(stock, "hub.adhar.io", gatewayServiceFQDN)
	if !strings.Contains(svcMode, gatewayServiceFQDN) {
		t.Error("the Service-mode rewrite lost its target")
	}
}
