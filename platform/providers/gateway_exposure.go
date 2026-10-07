package provider

import (
	"context"
	"strings"
)

// How the platform edge is exposed when there is no cloud load balancer.
//
// The platform Gateway is a Cilium Gateway, and on every cloud that runs a
// cloud-controller-manager that Gateway gets a Service of type LoadBalancer: a
// real address, an A record from external-dns, a Let's Encrypt certificate, and
// URLs that answer on 443. That is the shape everything downstream assumes.
//
// Civo in compute mode has no CCM — the Civo cloud-controller-manager resolves
// CIVO_CLUSTER_ID against the MANAGED Kubernetes API, which a kubeadm cluster on
// plain instances does not have, so it nil-panics and never initialises a node.
// Without a CCM there is no LoadBalancer Service, and a live bring-up (2026-10-06)
// therefore ended with a healthy platform whose every URL was unreachable: no
// address on the Gateway, nothing listening on 443, and the provider firewall
// open only on 22, 6443 and the node-port range. The same is true of the custom
// (bring-your-own-hosts) provider, which has no cloud API at all.
//
// The fix is Cilium's Gateway API HOST NETWORK mode: Envoy binds the listener
// ports in the node's own network namespace, so the Gateway is reachable on
// :80/:443 of every node and no Service — and no cloud load balancer — is
// involved. Three things have to agree for it to work, and the first attempt at
// this got one of three (see RewriteCiliumGatewayHostNetwork):
//
//  1. `gateway-api-hostnetwork-enabled: "true"` in cilium-config,
//  2. NET_BIND_SERVICE on the cilium-envoy container — without it Envoy cannot
//     bind a port below 1024, the Gateway reports Programmed with node addresses,
//     and NOTHING listens on 443,
//  3. `--keep-cap-net-bind-service` on cilium-envoy-starter, which otherwise
//     drops that capability before exec'ing Envoy.
//
// And two things outside Cilium: the provider firewall has to open 80 and 443,
// and external-dns has to publish the nodes' PUBLIC addresses (the Gateway's
// status carries the private ones, which are useless in a public zone — and
// `--policy=upsert-only` means a wrong record is never retracted).

// GatewayHostNetworkRequired reports whether this cluster needs the Gateway on
// the host network because nothing will give it a load-balancer address.
//
// Deliberately NOT "does this provider have a CCM": kind has no CCM either and
// must stay on NodePort, because the local flow maps host 8080/8443 to the
// pinned node ports 30080/30443. And a PROVIDED cluster is the operator's —
// its Cilium is configured by them, and silently switching their Gateway data
// path to the host network is not ours to do.
func GatewayHostNetworkRequired(providerType, clusterMode string) bool {
	switch normalizeProviderName(providerType) {
	case "civo":
		// Civo's CCM only works against its managed Kubernetes.
		return !ClusterModeIsManaged(clusterMode) && ClusterLifecycleIsOurs(clusterMode)
	case "custom":
		// Bring-your-own hosts: no cloud API, no load balancer, ever.
		return true
	default:
		return false
	}
}

// normalizeProviderName folds the spellings a provider can arrive under.
func normalizeProviderName(providerType string) string {
	switch p := strings.ToLower(strings.TrimSpace(providerType)); p {
	case "do", "digital-ocean":
		return "digitalocean"
	case "gke", "google", "googlecloud":
		return "gcp"
	case "eks", "amazon":
		return "aws"
	case "aks":
		return "azure"
	case "byo", "on-premises", "onprem":
		return "custom"
	default:
		return p
	}
}

// GatewayAddressProvider is an OPTIONAL capability: a provider that can say
// which addresses the platform edge is reachable on when there is no load
// balancer. These are the addresses external-dns publishes, so they must be the
// PUBLIC ones — a private address in a public zone is a record that resolves and
// never connects.
type GatewayAddressProvider interface {
	GatewayAddresses(ctx context.Context, clusterID string) ([]string, error)
}
