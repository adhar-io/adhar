package adharplatform

// The platform edge on a cluster with no load balancer.
//
// When provider.GatewayHostNetworkRequired is true, Cilium's Gateway API runs in
// host-network mode: Envoy binds :80/:443 in each node's own network namespace
// and Cilium creates NO Service for the Gateway. That leaves two things for the
// platform to supply, and without either one the URLs still do not work:
//
//   - PUBLIC DNS. The Gateway's status addresses are the nodes' PRIVATE IPs.
//     external-dns refuses to publish RFC1918 addresses into a public zone (and
//     with --policy=upsert-only a wrong record is never retracted), so the
//     public addresses have to be handed to it explicitly. The
//     `external-dns.alpha.kubernetes.io/target` annotation on the Gateway
//     OVERRIDES the status addresses — verified against external-dns v0.15.1,
//     source/gateway.go: getTargetsFromTargetAnnotation(gw.gateway.Annotations).
//
//   - IN-CLUSTER RESOLUTION. Pods resolve the platform hostnames too, and they
//     must not be sent to a public IP: that address is NAT'd to a node's own
//     private address, and hairpin NAT through the cloud's edge generally does
//     not work. CoreDNS therefore rewrites `*.<host>` to a Service — but in
//     host-network mode there is no Gateway Service to point at. So the platform
//     creates a selector-less shim Service whose EndpointSlice lists the nodes'
//     INTERNAL addresses on 80/443, which is exactly where the host-network
//     Envoy is listening.
//
// Why this matters more than it looks: every oauth2-proxy performs OIDC
// Discovery against https://keycloak.<host>/... at start-up and EXITS when the
// lookup fails. On the first live Civo compute bring-up that was twenty-odd SSO
// proxies in CrashLoopBackOff on an otherwise healthy platform.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
)

const (
	// gatewayHostNetworkServiceName is the shim Service that stands in for the
	// Gateway Service that host-network mode does not create.
	// gatewayResourceName is the platform Gateway (resources/gateway/*.yaml).
	gatewayResourceName = "adhar-gateway"

	gatewayHostNetworkServiceName = "adhar-gateway-hostnetwork"
	gatewayHostNetworkServiceFQDN = gatewayHostNetworkServiceName + "." +
		globals.AdharSystemNamespace + ".svc.cluster.local"

	// externalDNSTargetAnnotation overrides the addresses external-dns would
	// otherwise read from the Gateway's status.
	externalDNSTargetAnnotation = "external-dns.alpha.kubernetes.io/target"
)

// gatewayPublicAddresses returns the edge addresses the CLI recorded at
// bootstrap, in order. Empty when the cluster has a load balancer (nothing
// records them) or when the ConfigMap is absent.
func (r *AdharPlatformReconciler) gatewayPublicAddresses(ctx context.Context) []string {
	cm := &corev1.ConfigMap{}
	key := client.ObjectKey{Name: globals.ClusterSpecConfigMapName, Namespace: globals.AdharSystemNamespace}
	if err := r.Get(ctx, key, cm); err != nil {
		return nil
	}
	var out []string
	for _, addr := range strings.Split(cm.Data[globals.ClusterSpecGatewayAddressesKey], ",") {
		if addr = strings.TrimSpace(addr); addr != "" {
			out = append(out, addr)
		}
	}
	return out
}

// ensureGatewayExternalDNSTarget hands external-dns the public addresses of the
// edge. Without it the only candidates are the nodes' private IPs, which
// external-dns is configured to refuse — so the platform's hostnames get no
// public record at all.
func (r *AdharPlatformReconciler) ensureGatewayExternalDNSTarget(ctx context.Context, addresses []string) error {
	if len(addresses) == 0 {
		return fmt.Errorf("no public addresses recorded for the platform edge " +
			"(the CLI writes them into " + globals.ClusterSpecConfigMapName + " at bootstrap)")
	}
	logger := log.FromContext(ctx)

	gw := &unstructured.Unstructured{}
	gw.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "gateway.networking.k8s.io",
		Version: "v1",
		Kind:    "Gateway",
	})
	gw.SetName(gatewayResourceName)
	gw.SetNamespace(globals.AdharSystemNamespace)
	gw.SetAnnotations(map[string]string{externalDNSTargetAnnotation: strings.Join(addresses, ",")})

	if err := r.Patch(ctx, gw, client.Apply,
		client.FieldOwner(v1alpha1.FieldManager), client.ForceOwnership); err != nil {
		return fmt.Errorf("annotating the Gateway with its public addresses: %w", err)
	}
	logger.Info("Platform edge addresses published through external-dns", "targets", addresses)
	return nil
}

// ensureGatewayHostNetworkShim creates the Service (and its EndpointSlice) that
// in-cluster clients reach the host-network Envoy through.
//
// Selector-less on purpose: the endpoints are the NODES, not pods. The node
// addresses come from the Node objects rather than from the recorded public
// ones, because in-cluster traffic has to stay inside the cluster — sending a
// pod to the public address relies on hairpin NAT at the cloud edge.
func (r *AdharPlatformReconciler) ensureGatewayHostNetworkShim(ctx context.Context, resource *v1alpha1.AdharPlatform) error {
	logger := log.FromContext(ctx)

	nodes := &corev1.NodeList{}
	if err := r.List(ctx, nodes); err != nil {
		return fmt.Errorf("listing nodes for the gateway shim: %w", err)
	}
	var addresses []string
	for i := range nodes.Items {
		if !nodeIsReady(&nodes.Items[i]) {
			continue
		}
		for _, addr := range nodes.Items[i].Status.Addresses {
			if addr.Type == corev1.NodeInternalIP && addr.Address != "" {
				addresses = append(addresses, addr.Address)
				break
			}
		}
	}
	if len(addresses) == 0 {
		return fmt.Errorf("no Ready node has an internal address, so the platform edge has no in-cluster endpoint")
	}
	sort.Strings(addresses)

	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{
		Name:      gatewayHostNetworkServiceName,
		Namespace: globals.AdharSystemNamespace,
	}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		if svc.Labels == nil {
			svc.Labels = map[string]string{}
		}
		svc.Labels["app.kubernetes.io/name"] = gatewayHostNetworkServiceName
		svc.Labels["app.kubernetes.io/managed-by"] = "adhar"
		svc.Spec.Type = corev1.ServiceTypeClusterIP
		// No selector: this Service's endpoints are maintained below.
		svc.Spec.Selector = nil
		svc.Spec.Ports = []corev1.ServicePort{
			{Name: "http", Port: 80, TargetPort: intstr.FromInt32(80), Protocol: corev1.ProtocolTCP},
			{Name: "https", Port: 443, TargetPort: intstr.FromInt32(443), Protocol: corev1.ProtocolTCP},
		}
		return controllerutil.SetControllerReference(resource, svc, r.Scheme)
	}); err != nil {
		return fmt.Errorf("creating the gateway shim Service: %w", err)
	}

	slice := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{
		Name:      gatewayHostNetworkServiceName,
		Namespace: globals.AdharSystemNamespace,
	}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, slice, func() error {
		if slice.Labels == nil {
			slice.Labels = map[string]string{}
		}
		// This label is what binds the slice to the Service; without it the
		// Service has no endpoints and the name resolves to nothing.
		slice.Labels[discoveryv1.LabelServiceName] = gatewayHostNetworkServiceName
		slice.Labels["app.kubernetes.io/managed-by"] = "adhar"
		slice.AddressType = discoveryv1.AddressTypeIPv4
		slice.Endpoints = nil
		for _, addr := range addresses {
			slice.Endpoints = append(slice.Endpoints, discoveryv1.Endpoint{
				Addresses:  []string{addr},
				Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(true)},
			})
		}
		slice.Ports = []discoveryv1.EndpointPort{
			{Name: ptr.To("http"), Port: ptr.To(int32(80)), Protocol: ptr.To(corev1.ProtocolTCP)},
			{Name: ptr.To("https"), Port: ptr.To(int32(443)), Protocol: ptr.To(corev1.ProtocolTCP)},
		}
		return controllerutil.SetControllerReference(resource, slice, r.Scheme)
	}); err != nil {
		return fmt.Errorf("creating the gateway shim EndpointSlice: %w", err)
	}

	logger.Info("In-cluster endpoint for the host-network gateway", "service", gatewayHostNetworkServiceFQDN, "nodes", addresses)
	return nil
}

// nodeIsReady reports whether a node is currently Ready. An unready node still
// has Envoy on it, but sending in-cluster traffic there is a connection that
// hangs rather than one that fails over.
func nodeIsReady(node *corev1.Node) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// nodeAddressesChanged reports whether the shim's endpoints still describe the
// cluster, so a scaled cluster does not keep routing to a node that is gone.
func (r *AdharPlatformReconciler) gatewayShimNeedsRefresh(ctx context.Context) bool {
	slice := &discoveryv1.EndpointSlice{}
	key := types.NamespacedName{Name: gatewayHostNetworkServiceName, Namespace: globals.AdharSystemNamespace}
	if err := r.Get(ctx, key, slice); err != nil {
		return true
	}
	return len(slice.Endpoints) == 0
}
