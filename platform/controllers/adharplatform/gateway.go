package adharplatform

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"
	"time"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/k8s"
	provider "adhar-io/adhar/platform/providers"
	"adhar-io/adhar/platform/utils/files"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// gatewayClassConfigCRDName is the Cilium CRD backing the CiliumGatewayClassConfig
// referenced by the adhar GatewayClass. It is registered asynchronously by the
// Cilium operator.
const gatewayClassConfigCRDName = "ciliumgatewayclassconfigs.cilium.io"

//go:embed resources/gateway-api
//go:embed resources/gateway
var gatewayFS embed.FS

const (
	// gatewayServiceName is the Service Cilium generates for the adhar-gateway
	// Gateway (named cilium-gateway-<gateway-name>).
	gatewayServiceName = "cilium-gateway-adhar-gateway"
	// Fixed node ports so the Kind static host port-mapping (8080/8443 ->
	// 30080/30443) keeps working. CiliumGatewayClassConfig can select NodePort
	// service type but cannot pin port numbers, so the controller patches them.
	// The adhar-controlplane name of the Gateway Service, used by the CoreDNS rewrite.
	gatewayServiceFQDN   = "cilium-gateway-adhar-gateway.adhar-system.svc.cluster.local"
	gatewayHTTPNodePort  = 30080
	gatewayHTTPSNodePort = 30443
	// The 8443 listener is pinned to the SAME node port number so that, on the
	// node itself, https://keycloak.<host>:8443 (which resolves to loopback)
	// reaches the Gateway. kube-apiserver performs OIDC discovery against that
	// exact issuer URL, and cannot be given a different port. Requires the
	// widened service-node-port-range in the Kind config.
	gatewayAltHTTPSPort     = 8443
	gatewayAltHTTPSNodePort = 8443
)

// RawGatewayInstallResources returns the Gateway/GatewayClass manifest so it can
// be pushed through the GitOps flow like other core packages.
func RawGatewayInstallResources(templateData any, config v1alpha1.PackageCustomization, scheme *runtime.Scheme) ([][]byte, error) {
	// config.FilePath, when set, points at a user-provided override file on
	// local disk; empty means "no overrides".
	return k8s.BuildCustomizedManifests(config.FilePath, "resources/gateway", gatewayFS, scheme, templateData)
}

// ReconcileGatewayAPICRDs installs the upstream Gateway API CRDs. These must be
// present before Cilium starts with Gateway API support enabled.
func (r *AdharPlatformReconciler) ReconcileGatewayAPICRDs(ctx context.Context, req ctrl.Request, resource *v1alpha1.AdharPlatform) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling Gateway API CRDs")

	// A cluster that already carries the Gateway API — Civo's managed k3s ships
	// the STANDARD channel with Cilium, plus the upstream `safe-upgrades`
	// ValidatingAdmissionPolicy — refuses the platform's EXPERIMENTAL bundle
	// outright: "Installing experimental CRDs on top of standard channel CRDs is
	// prohibited". Every reconcile then failed at this step and the bring-up
	// never reached Argo CD (mum1, 2026-10-17). The platform's own objects
	// (Gateway, GatewayClass, HTTPRoute, ReferenceGrant, GRPCRoute) are all
	// standard, so the cluster's CRDs are used as they are; packages that
	// declare experimental kinds (for example, agentgateway's TLSRoute) stay
	// OutOfSync there, which is a missing feature, not a broken cluster.
	if foreign, detail := r.foreignGatewayAPICRDs(ctx); foreign {
		logger.Info("The cluster already provides the Gateway API CRDs; the platform's bundle is not applied over them", "existing", detail)
		return ctrl.Result{}, nil
	}
	manifestBytes, err := gatewayFS.ReadFile("resources/gateway-api/crds.yaml")
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reading gateway-api crds manifest: %w", err)
	}
	if err := r.applyManifest(ctx, manifestBytes, resource, "Gateway API CRDs"); err != nil {
		return ctrl.Result{}, fmt.Errorf("applying gateway-api crds: %w", err)
	}
	logger.Info("Gateway API CRDs installed")
	return ctrl.Result{}, nil
}

const (
	gatewayAPIChannelAnnotation = "gateway.networking.k8s.io/channel"
	gatewayAPIBundleAnnotation  = "gateway.networking.k8s.io/bundle-version"
	gatewayAPIProbeCRD          = "gateways.gateway.networking.k8s.io"
)

// foreignGatewayAPICRDs reports whether another party installed the Gateway API
// CRDs from the STANDARD channel. The platform's bundle is experimental; the
// upstream safe-upgrades policy forbids layering it over standard, and nothing
// the platform itself creates needs the experimental kinds. An absent CRD, or
// one already on the experimental channel (the platform's own earlier apply),
// is not foreign.
func (r *AdharPlatformReconciler) foreignGatewayAPICRDs(ctx context.Context) (bool, string) {
	crd := &unstructured.Unstructured{}
	crd.SetAPIVersion("apiextensions.k8s.io/v1")
	crd.SetKind("CustomResourceDefinition")
	if err := r.Get(ctx, client.ObjectKey{Name: gatewayAPIProbeCRD}, crd); err != nil {
		return false, ""
	}
	ann := crd.GetAnnotations()
	if ann[gatewayAPIChannelAnnotation] != "standard" {
		return false, ""
	}
	return true, fmt.Sprintf("%s channel %s", ann[gatewayAPIChannelAnnotation], ann[gatewayAPIBundleAnnotation])
}

// ReconcileGateway installs the platform GatewayClass, CiliumGatewayClassConfig
// and Gateway. On Kind it pins the generated Service's node ports so the host
// port-mapping continues to route to the platform; on cloud/on-prem providers
// it applies the production edge variant instead (LoadBalancer Service,
// cert-manager-managed listener certificate — roadmap P1.3).
func (r *AdharPlatformReconciler) ReconcileGateway(ctx context.Context, req ctrl.Request, resource *v1alpha1.AdharPlatform) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// An unset provider means a locally created platform (Kind).
	isKind := resource.Spec.Provider == v1alpha1.ProviderKind || resource.Spec.Provider == ""
	logger.Info("Reconciling Gateway (Cilium Gateway API)", "kindProvider", isKind)

	manifestPath := "resources/gateway/gateway.yaml"
	if !isKind {
		manifestPath = "resources/gateway/gateway-cloud.yaml"
	}
	manifestBytes, err := gatewayFS.ReadFile(manifestPath)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reading gateway manifest %s: %w", manifestPath, err)
	}

	// The Kind gateway.yaml includes a CiliumGatewayClassConfig, whose CRD
	// (ciliumgatewayclassconfigs.cilium.io) is registered asynchronously by the
	// Cilium operator — it may not exist yet on the pass immediately after Cilium
	// installs. If we apply gateway.yaml before that CRD is Established, the config
	// object does not land, the GatewayClass is rejected (InvalidParameters), the
	// generated Service defaults to LoadBalancer instead of NodePort, and the
	// Gateway never programs (leaving ArgoCD/Gitea unreachable on the Kind host
	// ports). Wait (bounded) for the CRD before applying so the config always
	// lands on the first successful pass. Non-fatal: on timeout we proceed and
	// rely on the outer reconcile loop (Status.Gateway.Available stays false) to
	// re-apply on a later pass once the CRD registers.
	if isKind {
		if err := r.waitForGatewayClassConfigCRD(ctx); err != nil {
			logger.Info("CiliumGatewayClassConfig CRD not Established yet; applying anyway and will re-reconcile", "error", err)
		}
	}
	if !isKind {
		// The cloud variant carries the platform wildcard hostname on its
		// HTTPS listener for cert-manager.
		manifestBytes, err = files.ApplyTemplate(manifestBytes, r.Config)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("rendering gateway manifest %s: %w", manifestPath, err)
		}
	}
	if err := r.applyManifest(ctx, manifestBytes, resource, "Gateway"); err != nil {
		return ctrl.Result{}, fmt.Errorf("applying gateway manifest: %w", err)
	}

	// Kind only: Cilium creates the gateway Service asynchronously once the
	// Gateway is accepted. Pin its node ports so the Kind host port-mapping
	// (8080/8443 -> 30080/30443) works. This is intentionally NON-FATAL: on a
	// cold cluster Cilium may need more than one reconcile to program the
	// Gateway and create the Service. Failing here must not abort the core
	// install (ArgoCD/Gitea/Crossplane). We leave Status.Gateway.Available
	// unset so the core-install gate re-runs this reconciler until the Service
	// is pinned, while the rest of the install still proceeds this pass.
	// Cloud providers use a LoadBalancer Service, where port allocation is the
	// LB implementation's concern and pinning would never converge.
	if isKind {
		if err := r.pinGatewayNodePorts(ctx); err != nil {
			logger.Info("Gateway Service node ports not pinned yet; will retry on the next reconcile", "error", err)
			return ctrl.Result{}, nil
		}
	}

	// A cloud cluster whose gateway Service never gets a LoadBalancer address
	// cannot resolve its own hostnames, and that breaks the platform from the
	// INSIDE: every oauth2-proxy does OIDC Discovery against
	// https://keycloak.<host>/... at startup and exits when the lookup fails.
	// Measured on a live Civo compute bring-up (2026-10-06) — twenty-odd SSO
	// proxies in CrashLoopBackOff with `no such host`, their applications
	// Degraded, on an otherwise healthy platform. Non-fatal: it is a
	// reachability improvement, not a prerequisite for the core install.
	// A cluster with no cloud load balancer serves the edge from the nodes
	// themselves (Cilium Gateway host-network mode, turned on in ReconcileCilium).
	// Cilium creates no Service in that mode, so the two things a Service
	// normally provides have to be supplied here: a public DNS target for
	// external-dns, and an adhar-controlplane endpoint for CoreDNS to point at. Both are
	// non-fatal — they are reachability, not a prerequisite for the core install.
	hostNetwork := !isKind && r.gatewayHostNetworkRequired(ctx)
	if hostNetwork {
		if err := r.ensureGatewayExternalDNSTarget(ctx, r.gatewayPublicAddresses(ctx)); err != nil {
			logger.Info("Platform hostnames will have no public DNS record", "error", err)
		}
		if err := r.ensureGatewayHostNetworkShim(ctx, resource); err != nil {
			logger.Info("No adhar-controlplane endpoint for the host-network gateway; platform hostnames will not resolve from pods", "error", err)
		}
	}

	if !isKind {
		if err := r.ensureGatewayHostResolves(ctx, hostNetwork); err != nil {
			logger.Info("Could not point CoreDNS at the gateway; platform hostnames may not resolve adhar-controlplane", "error", err)
		}
	}

	resource.Status.Gateway.Available = true
	logger.Info("Gateway reconciliation completed successfully")
	return ctrl.Result{}, nil
}

// waitForGatewayClassConfigCRD blocks (bounded) until the Cilium
// CiliumGatewayClassConfig CRD is Established, so the CiliumGatewayClassConfig
// object in gateway.yaml can actually be created. Returns an error on timeout;
// callers treat that as non-fatal and let the reconcile loop retry.
func (r *AdharPlatformReconciler) waitForGatewayClassConfigCRD(ctx context.Context) error {
	logger := log.FromContext(ctx)
	// Wait up to ~3min, polling every 2s. The Cilium operator registers this CRD
	// asynchronously after it starts; establishing it BEFORE gateway.yaml is
	// applied is what makes the generated Service come up as NodePort on the first
	// pass. Waiting long enough here (breaking early the moment it establishes)
	// avoids the far costlier alternative: applying gateway.yaml too early, getting
	// a LoadBalancer Service, and then cycling through full installFoundation
	// requeues (each re-running this wait + the node-port pin) for minutes.
	for i := 0; i < 90; i++ { // up to ~180s
		var crd unstructured.Unstructured
		crd.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "apiextensions.k8s.io",
			Version: "v1",
			Kind:    "CustomResourceDefinition",
		})
		err := r.Get(ctx, client.ObjectKey{Name: gatewayClassConfigCRDName}, &crd)
		if err == nil && crdEstablished(&crd) {
			logger.Info("CiliumGatewayClassConfig CRD is Established")
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("CRD %s not Established within timeout", gatewayClassConfigCRDName)
}

// crdEstablished reports whether a CustomResourceDefinition (as unstructured)
// carries an Established=True status condition.
func crdEstablished(crd *unstructured.Unstructured) bool {
	conds, found, err := unstructured.NestedSlice(crd.Object, "status", "conditions")
	if err != nil || !found {
		return false
	}
	for _, c := range conds {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if cond["type"] == "Established" && cond["status"] == "True" {
			return true
		}
	}
	return false
}

// pinGatewayNodePorts waits for the Cilium-generated gateway Service and patches
// its HTTP/HTTPS node ports to the fixed values Kind expects.
func (r *AdharPlatformReconciler) pinGatewayNodePorts(ctx context.Context) error {
	logger := log.FromContext(ctx)
	logger.Info("Waiting for Cilium gateway Service to pin node ports...", "service", gatewayServiceName)

	// Cilium owns this Service and re-reconciles it, so a plain Update can hit a
	// resource-version conflict; retry.RetryOnConflict re-reads and re-applies.
	//
	// Wait up to ~4min in THIS pass, polling every 3s. Converging here (as soon as
	// Cilium has created the NodePort Service) is far cheaper than timing out and
	// letting installFoundation requeue: each requeue re-runs the whole foundation
	// (CRD wait + this pin), so a premature timeout turns a one-time ~1-2min wait
	// into minutes of cycling — the actual cause of the slow "Cilium & Gateway"
	// phase. The loop still breaks the instant the Service is pinned.
	for i := 0; i < 80; i++ { // up to ~240s
		var svc corev1.Service
		if err := r.Get(ctx, types.NamespacedName{Name: gatewayServiceName, Namespace: globals.AdharSystemNamespace}, &svc); err != nil || svc.Spec.Type != corev1.ServiceTypeNodePort {
			time.Sleep(3 * time.Second)
			continue
		}
		if gatewayNodePortsPinned(&svc) {
			logger.Info("Gateway Service node ports already pinned")
			return nil
		}
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var cur corev1.Service
			if getErr := r.Get(ctx, types.NamespacedName{Name: gatewayServiceName, Namespace: globals.AdharSystemNamespace}, &cur); getErr != nil {
				return getErr
			}
			setGatewayNodePorts(&cur)
			return r.Update(ctx, &cur)
		})
		if err == nil {
			logger.Info("Pinned gateway Service node ports", "http", gatewayHTTPNodePort, "https", gatewayHTTPSNodePort)
			return nil
		}
		logger.Info("Failed to pin gateway node ports (will retry)", "error", err)
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("gateway Service %s not ready as NodePort within timeout", gatewayServiceName)
}

// gatewayNodePortsPinned reports whether the Service's HTTP/HTTPS node ports
// already match the fixed values Kind maps host ports to.
func gatewayNodePortsPinned(svc *corev1.Service) bool {
	for _, p := range svc.Spec.Ports {
		switch p.Port {
		case 80:
			if p.NodePort != gatewayHTTPNodePort {
				return false
			}
		case 443:
			if p.NodePort != gatewayHTTPSNodePort {
				return false
			}
		case gatewayAltHTTPSPort:
			if p.NodePort != gatewayAltHTTPSNodePort {
				return false
			}
		}
	}
	return true
}

// setGatewayNodePorts pins the HTTP(80)/HTTPS(443) listener node ports in place.
func setGatewayNodePorts(svc *corev1.Service) {
	for idx := range svc.Spec.Ports {
		switch svc.Spec.Ports[idx].Port {
		case 80:
			svc.Spec.Ports[idx].NodePort = gatewayHTTPNodePort
		case 443:
			svc.Spec.Ports[idx].NodePort = gatewayHTTPSNodePort
		case gatewayAltHTTPSPort:
			svc.Spec.Ports[idx].NodePort = gatewayAltHTTPSNodePort
		}
	}
}

// insertGatewayRewrite adds a CoreDNS `rewrite` that resolves `*.<host>` to an
// adhar-controlplane Service name, returning the new Corefile and whether it changed.
//
// `target` differs by how the edge is exposed: the Cilium Gateway Service
// normally, and the host-network shim Service (which points at the nodes' own
// addresses) on a cluster where Cilium's Gateway binds the host network and
// creates no Service at all.
//
// It is a pure function so the Corefile surgery can be tested without a
// cluster: a malformed Corefile takes DNS down for everything.
//
// The rewrite goes immediately BEFORE `forward`, because `rewrite stop` has to
// be evaluated before the query leaves for the upstream resolver. CoreDNS's own
// `reload` directive picks the ConfigMap change up within ~30s, so nothing has
// to restart it.
func insertGatewayRewrite(corefile, host, target string) (string, bool) {
	if host == "" || target == "" || strings.Contains(corefile, target) {
		return corefile, false
	}
	const forwardPrefix = "forward . /etc/resolv.conf"
	lines := strings.Split(corefile, "\n")
	for i, l := range lines {
		if !strings.HasPrefix(strings.TrimSpace(l), forwardPrefix) {
			continue
		}
		indent := l[:len(l)-len(strings.TrimLeft(l, " \t"))]
		block := []string{
			indent + "rewrite stop {",
			indent + "    name regex (.*)\\." + strings.ReplaceAll(host, ".", "\\.") + " " + target + " answer auto",
			indent + "}",
		}
		out := append([]string{}, lines[:i]...)
		out = append(out, block...)
		out = append(out, lines[i:]...)
		return strings.Join(out, "\n"), true
	}
	// No forward directive: appending a rewrite to an unknown layout risks
	// breaking resolution, so leave it alone and say nothing changed.
	return corefile, false
}

// ensureGatewayHostResolves makes the platform's own hostnames resolve INSIDE
// the cluster, which they otherwise would not on a cluster with no load
// balancer.
//
// Two shapes:
//
//   - Host-network mode (no Gateway Service exists at all): point CoreDNS at
//     the shim Service, whose endpoints are the nodes' internal addresses —
//     where the host-network Envoy is listening.
//   - A Gateway Service with no LoadBalancer address: point CoreDNS at the
//     Service itself.
//
// It does NOTHING when the Service has a real address: there, external-dns
// publishes a record and the public name resolves everywhere.
func (r *AdharPlatformReconciler) ensureGatewayHostResolves(ctx context.Context, hostNetwork bool) error {
	logger := log.FromContext(ctx)

	target := gatewayServiceFQDN
	if hostNetwork {
		target = gatewayHostNetworkServiceFQDN
	} else {
		var svc corev1.Service
		if err := r.Get(ctx, types.NamespacedName{Name: gatewayServiceName, Namespace: globals.AdharSystemNamespace}, &svc); err != nil {
			return fmt.Errorf("reading the gateway Service: %w", err)
		}
		for _, ing := range svc.Status.LoadBalancer.Ingress {
			if ing.IP != "" || ing.Hostname != "" {
				return nil // a real address exists; public DNS is the path
			}
		}
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cm corev1.ConfigMap
		if err := r.Get(ctx, types.NamespacedName{Name: "coredns", Namespace: "kube-system"}, &cm); err != nil {
			return err
		}
		updated, changed := insertGatewayRewrite(cm.Data["Corefile"], r.Config.Host, target)
		if !changed {
			return nil
		}
		cm.Data["Corefile"] = updated
		if err := r.Update(ctx, &cm); err != nil {
			return err
		}
		logger.Info("CoreDNS now resolves the platform hostnames to the gateway", "host", r.Config.Host, "target", target)
		return nil
	})
}

// ── Cilium's Gateway API controllers can stop for good ──────────────────────
//
// The cilium-operator decides ONCE, at start-up, whether to run its Gateway API
// controllers: it checks that the Gateway API CRDs are served, and an answer it
// does not expect is final for the life of the process. On the AWS cluster
// (2026-10-30) the API server was re-created 23 minutes into `adhar up` (the
// day-2 OIDC step rewrites the kube-apiserver static pod) and refused
// connections for ~25 s. The operator lost its lease and restarted, and the
// first answer it got once the API was back was an RBAC "forbidden" from an
// authorizer that had not warmed up — `Required GatewayAPI resources are not
// found` — so it ran WITHOUT its gatewayclass, gateway and httproute
// controllers from then on. Every HTTPRoute created afterwards (penpot, plane,
// loki) got no status at all for 27 minutes: Argo CD showed those apps
// Progressing, and their hostnames reset the TLS handshake because Envoy had no
// filter chain for them. `kubectl rollout restart deployment/cilium-operator`
// fixed it in seconds.
//
// The signal is a route attached to the platform Gateway that Cilium has not
// answered. A healthy operator writes a parent status within seconds, and
// Accepted=False is an answer too; a route with NO status entry for the
// Gateway after a few minutes, under an operator that has been ready at least
// as long, means nothing is listening. Whose Cilium it is to restart follows
// ReconcileCilium: the platform's own install in every mode, the cluster's own
// on a cluster whose lifecycle is ours (adoptForeignCilium restarts that one
// too), and never the Cilium of a provided cluster, which is its operator's.

const (
	// gatewayRouteAnswerGrace is how long a route attached to the platform
	// Gateway may go unanswered — counted from the later of the route's last
	// change and the Gateway's creation, under an operator that has been ready
	// at least as long — before the operator is taken to be running without
	// its Gateway API controllers.
	gatewayRouteAnswerGrace = 3 * time.Minute

	// ciliumOperatorRestartInterval allows one restart per interval, so a cause
	// a restart does not cure costs one restart every ten minutes rather than a
	// restart loop. A restart anyone made counts: the adoption path's, and the
	// `kubectl rollout restart` a person reaches for first.
	ciliumOperatorRestartInterval = 10 * time.Minute

	// gatewayRouteScanInterval spaces out the route scan, the one check here
	// that is a live cluster-wide LIST (unstructured objects are not served
	// from the cache) — on a control plane this platform already strains.
	gatewayRouteScanInterval = time.Minute

	ciliumOperatorName           = "cilium-operator"
	kubectlRestartedAtAnnotation = "kubectl.kubernetes.io/restartedAt"
	gatewayAPIGroup              = "gateway.networking.k8s.io"
)

// reconcileCiliumGatewayControllers restarts the cilium-operator when HTTPRoutes
// attached to the platform Gateway have gone unanswered (see above), at most
// once per ciliumOperatorRestartInterval, and says why in the log and in an
// Event on the AdharPlatform. Every gate before the route scan is a cached
// read, and the scan itself runs at most once a minute.
func (r *AdharPlatformReconciler) reconcileCiliumGatewayControllers(ctx context.Context, resource *v1alpha1.AdharPlatform) error {
	namespace, err := r.restartableCiliumNamespace(ctx)
	if err != nil || namespace == "" {
		return err
	}
	operator := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Name: ciliumOperatorName, Namespace: namespace}, operator); err != nil {
		return client.IgnoreNotFound(err)
	}
	// An operator that is not running has a different problem, and a restart
	// is not its repair. Read from the replica count, not the Available
	// condition: the chart sets maxUnavailable to 100%, so with one replica
	// that condition reads True when no pod is up at all.
	if operator.Status.AvailableReplicas == 0 {
		return nil
	}
	now := time.Now()
	if now.Sub(lastRestartOf(operator)) < ciliumOperatorRestartInterval {
		return nil
	}
	// An operator that came up less than the grace ago has not had its chance
	// to answer yet — and the outage that wedges it is also what restarts it.
	readySince, err := r.podsReadySince(ctx, operator)
	if err != nil || readySince.IsZero() || now.Sub(readySince) < gatewayRouteAnswerGrace {
		return err
	}
	if now.Sub(r.gatewayRoutesScannedAt) < gatewayRouteScanInterval {
		return nil
	}
	r.gatewayRoutesScannedAt = now
	unanswered, err := r.unansweredGatewayRoutes(ctx, now)
	if err != nil || len(unanswered) == 0 {
		return err
	}

	if err := r.restartCiliumOperator(ctx, namespace, now.UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	why := fmt.Sprintf("%d HTTPRoute(s) attached to %s/%s had no status from Cilium after %s (%s): "+
		"the operator was running without its Gateway API controllers, and a restart starts them again",
		len(unanswered), globals.AdharSystemNamespace, gatewayResourceName, gatewayRouteAnswerGrace, sampleNames(unanswered, 3))
	log.FromContext(ctx).Info("Restarted the cilium-operator: "+why, "deployment", namespace+"/"+ciliumOperatorName)
	if r.Recorder != nil {
		r.Recorder.Eventf(resource, operator, corev1.EventTypeWarning, "GatewayRoutesUnanswered", "RestartCiliumOperator",
			"Restarted %s/%s: %s", namespace, ciliumOperatorName, why)
	}
	return nil
}

// restartableCiliumNamespace returns the namespace of the Cilium this platform
// may restart, or "" when the Cilium is not the platform's to restart — the
// same decision ReconcileCilium makes about installing it.
func (r *AdharPlatformReconciler) restartableCiliumNamespace(ctx context.Context) (string, error) {
	existing, err := r.foreignCilium(ctx)
	if err != nil {
		return "", err
	}
	if existing == nil {
		return globals.AdharSystemNamespace, nil
	}
	if provider.ClusterLifecycleIsOurs(r.clusterModeFromSpec(ctx)) {
		return existing.Namespace, nil
	}
	return "", nil
}

// lastRestartOf is the newest restart stamp on a workload's pod template: this
// controller's, or the one `kubectl rollout restart` writes. Zero when neither
// is there.
func lastRestartOf(deploy *appsv1.Deployment) time.Time {
	var last time.Time
	for _, key := range []string{restartedAtAnnotation, kubectlRestartedAtAnnotation} {
		if t, err := time.Parse(time.RFC3339, deploy.Spec.Template.Annotations[key]); err == nil && t.After(last) {
			last = t
		}
	}
	return last
}

// podsReadySince is when a Deployment's pods last became ready: the newest
// Ready transition among the pods that are ready now, so that every replica,
// whichever holds the lease, has had its chance. Zero when no pod is ready.
func (r *AdharPlatformReconciler) podsReadySince(ctx context.Context, deploy *appsv1.Deployment) (time.Time, error) {
	if deploy.Spec.Selector == nil {
		return time.Time{}, nil
	}
	selector, err := metav1.LabelSelectorAsSelector(deploy.Spec.Selector)
	if err != nil || selector.Empty() {
		return time.Time{}, err
	}
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(deploy.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return time.Time{}, err
	}
	var since time.Time
	for i := range pods.Items {
		if pods.Items[i].DeletionTimestamp != nil {
			continue
		}
		for _, c := range pods.Items[i].Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue && c.LastTransitionTime.After(since) {
				since = c.LastTransitionTime.Time
			}
		}
	}
	return since, nil
}

// unansweredGatewayRoutes names the HTTPRoutes attached to the platform Gateway
// that have no status entry for it, and have waited longer than the grace since
// the later of their last change and the Gateway's creation.
func (r *AdharPlatformReconciler) unansweredGatewayRoutes(ctx context.Context, now time.Time) ([]string, error) {
	gateway := &unstructured.Unstructured{}
	gateway.SetGroupVersionKind(schema.GroupVersionKind{Group: gatewayAPIGroup, Version: "v1", Kind: "Gateway"})
	key := types.NamespacedName{Name: gatewayResourceName, Namespace: globals.AdharSystemNamespace}
	if err := r.Get(ctx, key, gateway); err != nil {
		// No Gateway (or no Gateway API) yet: a route naming it is waiting on
		// that, not on Cilium.
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, err
	}
	gatewayCreated := gateway.GetCreationTimestamp().Time

	routes := &unstructured.UnstructuredList{}
	routes.SetGroupVersionKind(schema.GroupVersionKind{Group: gatewayAPIGroup, Version: "v1", Kind: "HTTPRouteList"})
	if err := r.List(ctx, routes); err != nil {
		return nil, err
	}
	var unanswered []string
	for i := range routes.Items {
		route := &routes.Items[i]
		if route.GetDeletionTimestamp() != nil || !routeAttachedToPlatformGateway(route) || routeAnsweredForPlatformGateway(route) {
			continue
		}
		waiting := routeChangedAt(route)
		if gatewayCreated.After(waiting) {
			waiting = gatewayCreated
		}
		if now.Sub(waiting) >= gatewayRouteAnswerGrace {
			unanswered = append(unanswered, route.GetNamespace()+"/"+route.GetName())
		}
	}
	sort.Strings(unanswered)
	return unanswered, nil
}

// routeAttachedToPlatformGateway reports whether any of the route's parentRefs
// names the platform Gateway.
func routeAttachedToPlatformGateway(route *unstructured.Unstructured) bool {
	refs, _, _ := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
	for _, raw := range refs {
		if ref, ok := raw.(map[string]any); ok && refersToPlatformGateway(ref, route.GetNamespace()) {
			return true
		}
	}
	return false
}

// routeAnsweredForPlatformGateway reports whether the route carries a status
// entry for the platform Gateway, whatever its conditions say. It looks for
// THAT entry rather than for any status: another implementation can write the
// status first — on the AWS cluster agentgateway stamped an empty one on every
// route the moment it was created.
func routeAnsweredForPlatformGateway(route *unstructured.Unstructured) bool {
	parents, _, _ := unstructured.NestedSlice(route.Object, "status", "parents")
	for _, raw := range parents {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if ref, ok := entry["parentRef"].(map[string]any); ok && refersToPlatformGateway(ref, route.GetNamespace()) {
			return true
		}
	}
	return false
}

// refersToPlatformGateway reports whether a parentRef names the platform
// Gateway, applying the Gateway API defaults: group gateway.networking.k8s.io,
// kind Gateway, and the route's own namespace.
func refersToPlatformGateway(ref map[string]any, routeNamespace string) bool {
	field := func(name, fallback string) string {
		if v, ok := ref[name].(string); ok && v != "" {
			return v
		}
		return fallback
	}
	return field("group", gatewayAPIGroup) == gatewayAPIGroup &&
		field("kind", "Gateway") == "Gateway" &&
		field("namespace", routeNamespace) == globals.AdharSystemNamespace &&
		field("name", "") == gatewayResourceName
}

// routeChangedAt is when the route was created or last changed — the moment
// Cilium was last given something to answer. Nothing records when the
// generation moved, so it is the newest write any manager made to the object
// itself (status writes excluded), which is never earlier than that.
func routeChangedAt(route *unstructured.Unstructured) time.Time {
	changed := route.GetCreationTimestamp().Time
	for _, entry := range route.GetManagedFields() {
		if entry.Subresource == "" && entry.Time != nil && entry.Time.After(changed) {
			changed = entry.Time.Time
		}
	}
	return changed
}

// sampleNames lists up to n names, and how many more there are.
func sampleNames(names []string, n int) string {
	if len(names) <= n {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:n], ", "), len(names)-n)
}
