package adharplatform

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
	provider "adhar-io/adhar/platform/providers"
)

//go:embed resources/cilium
var ciliumFS embed.FS

func (r *AdharPlatformReconciler) ReconcileCilium(ctx context.Context, req ctrl.Request, resource *v1alpha1.AdharPlatform) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling Cilium core package")

	// A Cilium THIS PLATFORM DID NOT INSTALL is the cluster's own, in every
	// mode, and installing ours on top of it does not produce two Ciliums — it
	// produces none. See the note above foreignCilium for what that cost.
	mode := r.clusterModeFromSpec(ctx)
	existing, err := r.foreignCilium(ctx)
	if err != nil {
		logger.Error(err, "Failed to check for an existing Cilium install")
		return ctrl.Result{}, fmt.Errorf("checking for an existing Cilium install: %w", err)
	}
	if existing != nil {
		logger.Info("The cluster already runs Cilium; the platform will not install its own",
			"daemonSet", existing.Namespace+"/"+existing.Name, "clusterMode", mode)
		if provider.ClusterLifecycleIsOurs(mode) {
			// This cluster is ours (we asked the cloud to build it WITH Cilium),
			// so the one feature the platform needs from it may be turned on, and
			// the damage an earlier release did may be repaired.
			if err := r.adoptForeignCilium(ctx, existing.Namespace); err != nil {
				logger.Info("Could not finish adopting the cluster's Cilium; the Gateway may not program",
					"error", err)
			}
		}
		return ctrl.Result{}, nil
	}
	logger.Info("No CNI on this cluster; installing Cilium", "clusterMode", mode)

	// Apply install.yaml
	ciliumManifestPath := "resources/cilium/install.yaml"
	manifestBytes, err := ciliumFS.ReadFile(ciliumManifestPath)
	if err != nil {
		logger.Error(err, "Failed to read Cilium install manifest", "path", ciliumManifestPath)
		return ctrl.Result{}, fmt.Errorf("reading cilium manifest %s: %w", ciliumManifestPath, err)
	}

	manifestBytes = r.rewriteCiliumAPIEndpoint(ctx, manifestBytes, resource)
	manifestBytes = rewriteCiliumClusterIdentity(ctx, manifestBytes, resource)

	// A cluster with no cloud load balancer reaches the platform edge on the
	// nodes themselves. Reported either way: "the platform's URLs do not answer"
	// and "the Gateway is on the host network" are the same symptom from the
	// outside, and the operator needs to know which one they have.
	if r.gatewayHostNetworkRequired(ctx) {
		patched, ok := rewriteCiliumGatewayHostNetwork(manifestBytes)
		if !ok {
			logger.Info("Gateway host-network mode is required but the embedded Cilium manifest no longer carries " +
				"all three anchors (hostnetwork config key, envoy starter args, envoy capabilities); " +
				"applying the stock manifest — the platform edge will have no address")
		} else {
			logger.Info("Cilium Gateway API in host-network mode: Envoy binds :80/:443 on every node " +
				"(this cluster has no cloud load balancer)")
			manifestBytes = patched
		}
	}

	if err := r.applyManifest(ctx, manifestBytes, resource, "Cilium install"); err != nil {
		logger.Error(err, "Failed to apply Cilium install manifest")
		return ctrl.Result{}, err
	}

	// Apply post-install.yaml
	ciliumPostInstallPath := "resources/cilium/post-install.yaml"
	postInstallBytes, err := ciliumFS.ReadFile(ciliumPostInstallPath)
	if err != nil {
		logger.Error(err, "Failed to read Cilium post-install manifest", "path", ciliumPostInstallPath)
		return ctrl.Result{}, fmt.Errorf("reading cilium post-install manifest %s: %w", ciliumPostInstallPath, err)
	}

	if err := r.applyManifest(ctx, postInstallBytes, resource, "Cilium post-install"); err != nil {
		logger.Error(err, "Failed to apply Cilium post-install manifest")
		return ctrl.Result{}, err
	}

	if err := r.reconcileClusterMeshAPIServer(ctx, resource); err != nil {
		logger.Error(err, "Failed to reconcile clustermesh-apiserver")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully reconciled Cilium core package")
	return ctrl.Result{}, nil
}

// ClusterMeshIdentity returns the Cilium cluster name and ID this platform
// should run with, falling back to the values baked into the embedded install
// manifest. Every cluster in a Cluster Mesh needs a unique pair: two clusters
// that both claim `adhar-mgmt`/1 cannot be connected at all, and identities
// allocated under the same ID would collide on the wire.
func ClusterMeshIdentity(resource *v1alpha1.AdharPlatform) (string, int32) {
	name, id := v1alpha1.DefaultClusterMeshName, v1alpha1.DefaultClusterMeshID
	if cm := resource.Spec.ClusterMesh; cm != nil {
		if cm.Name != "" {
			name = cm.Name
		}
		if cm.ID != 0 {
			id = cm.ID
		}
	}
	return name, id
}

// rewriteCiliumClusterIdentity stamps the platform's mesh identity into the
// generated manifest, which is rendered from Helm with the management
// cluster's defaults (adhar-mgmt / 1). Without this every Adhar cluster would
// present the same identity and `cilium clustermesh connect` would refuse to
// join them.
func rewriteCiliumClusterIdentity(ctx context.Context, manifest []byte, resource *v1alpha1.AdharPlatform) []byte {
	name, id := ClusterMeshIdentity(resource)
	if name == v1alpha1.DefaultClusterMeshName && id == v1alpha1.DefaultClusterMeshID {
		return manifest
	}
	manifest = bytes.ReplaceAll(manifest,
		[]byte(`cluster-name: "`+v1alpha1.DefaultClusterMeshName+`"`),
		[]byte(`cluster-name: "`+name+`"`))
	manifest = bytes.ReplaceAll(manifest,
		[]byte("cluster-name: "+v1alpha1.DefaultClusterMeshName+"\n"),
		[]byte("cluster-name: "+name+"\n"))
	manifest = bytes.ReplaceAll(manifest,
		[]byte(`cluster-id: "1"`),
		[]byte(`cluster-id: "`+strconv.Itoa(int(id))+`"`))
	log.FromContext(ctx).Info("Stamped Cilium cluster-mesh identity", "clusterName", name, "clusterID", id)
	return manifest
}

// reconcileClusterMeshAPIServer applies the clustermesh-apiserver when the
// platform opts into it. The manifest is rendered with a ClusterIP Service,
// which no peer can reach; the spec's serviceType/nodePort are stamped in so
// the other cluster has an address to dial. The apiserver's TLS is issued by
// the same `cilium-ca` that ships in install.yaml, so two Adhar clusters
// already trust each other's mesh endpoints.
func (r *AdharPlatformReconciler) reconcileClusterMeshAPIServer(ctx context.Context, resource *v1alpha1.AdharPlatform) error {
	cm := resource.Spec.ClusterMesh
	if cm == nil || cm.APIServer == nil || !cm.APIServer.Enabled {
		return nil
	}
	manifestBytes, err := ciliumFS.ReadFile("resources/cilium/clustermesh.yaml")
	if err != nil {
		return fmt.Errorf("reading cilium clustermesh manifest: %w", err)
	}
	manifestBytes = rewriteCiliumClusterIdentity(ctx, manifestBytes, resource)

	// The apiserver's identity is issued here, against the cluster's own
	// cilium-ca, and the manifest's copies are dropped: they are stamped with
	// the management cluster's etcd usernames and only work there.
	name, _ := ClusterMeshIdentity(resource)
	if err := r.ensureClusterMeshCerts(ctx, name); err != nil {
		return err
	}
	manifestBytes = stripClusterMeshCertSecrets(manifestBytes)

	svcType := cm.APIServer.ServiceType
	if svcType == "" {
		svcType = "NodePort"
	}
	if svcType != "ClusterIP" {
		nodePort := ""
		if svcType == "NodePort" {
			port := cm.APIServer.NodePort
			if port == 0 {
				port = v1alpha1.DefaultClusterMeshNodePort
			}
			nodePort = "\n    nodePort: " + strconv.Itoa(int(port))
		}
		manifestBytes = bytes.Replace(manifestBytes,
			[]byte("spec:\n  type: ClusterIP\n  selector:\n    k8s-app: clustermesh-apiserver\n  ports:\n  - port: 2379"),
			[]byte("spec:\n  type: "+svcType+"\n  selector:\n    k8s-app: clustermesh-apiserver\n  ports:\n  - port: 2379"+nodePort),
			1)
	}
	if err := r.applyManifest(ctx, manifestBytes, resource, "Cilium clustermesh-apiserver"); err != nil {
		return err
	}
	log.FromContext(ctx).Info("Applied clustermesh-apiserver", "serviceType", svcType)
	return nil
}

// stripClusterMeshCertSecrets removes the rendered TLS Secrets from the
// clustermesh manifest so the per-cluster certificates issued by
// ensureClusterMeshCerts are not overwritten on every reconcile.
func stripClusterMeshCertSecrets(manifest []byte) []byte {
	docs := bytes.Split(manifest, []byte("\n---\n"))
	kept := make([][]byte, 0, len(docs))
	for _, doc := range docs {
		drop := false
		for _, name := range clusterMeshCertSecrets {
			if bytes.Contains(doc, []byte("name: "+name+"\n")) {
				drop = true
				break
			}
		}
		if !drop {
			kept = append(kept, doc)
		}
	}
	return bytes.Join(kept, []byte("\n---\n"))
}

// RawCiliumInstallResources returns the raw Cilium installation manifest.
// TODO: Implement templateData and config processing if needed for Cilium manifests.
func RawCiliumInstallResources(templateData any, config v1alpha1.PackageCustomization, scheme *runtime.Scheme) ([][]byte, error) {
	ciliumManifestPath := "resources/cilium/install.yaml"
	manifestBytes, err := ciliumFS.ReadFile(ciliumManifestPath)
	if err != nil {
		return nil, fmt.Errorf("reading embedded cilium manifest %s: %w", ciliumManifestPath, err)
	}

	// For now, we return the manifest as a single item in a slice.
	// If the manifest is multi-document YAML, it should be split or handled accordingly
	// by the caller or a utility function.
	return [][]byte{manifestBytes}, nil
}

// rewriteCiliumAPIEndpoint substitutes the Kind-specific Kubernetes API
// endpoint baked into the generated Cilium manifest
// (KUBERNETES_SERVICE_HOST: adhar-control-plane) with the real API endpoint
// of the cluster being reconciled. Cilium agents run with kube-proxy
// replacement and host networking, so they cannot use the in-cluster service
// VIP and need a directly reachable endpoint. On Kind the baked-in node name
// is correct and is left untouched.
func (r *AdharPlatformReconciler) rewriteCiliumAPIEndpoint(ctx context.Context, manifest []byte, resource *v1alpha1.AdharPlatform) []byte {
	logger := log.FromContext(ctx)
	if resource.Spec.Provider == "" || resource.Spec.Provider == v1alpha1.ProviderKind {
		return manifest
	}

	cfg, err := ctrlconfig.GetConfig()
	if err != nil {
		logger.Error(err, "cannot determine API endpoint for Cilium; leaving manifest unchanged")
		return manifest
	}
	u, err := url.Parse(cfg.Host)
	if err != nil || u.Hostname() == "" {
		logger.Error(err, "cannot parse API host for Cilium", "host", cfg.Host)
		return manifest
	}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = "6443"
	}

	manifest = bytes.ReplaceAll(manifest,
		[]byte(`value: "adhar-control-plane"`),
		[]byte(`value: "`+host+`"`))

	// Multi-NIC cloud VMs defeat Cilium's device auto-detection (it derives
	// the direct routing device from the node InternalIP, which external
	// cloud-provider nodes lack until the CCM initializes them — after
	// Cilium). Pin the private-network interface per provider to break the
	// cycle.
	if dev, ok := cloudPrivateNIC[resource.Spec.Provider]; ok {
		manifest = bytes.ReplaceAll(manifest,
			[]byte("  routing-mode: tunnel"),
			[]byte("  routing-mode: tunnel\n  direct-routing-device: "+dev+"\n  devices: "+dev))
		logger.Info("Pinned Cilium network device for cloud provider", "provider", resource.Spec.Provider, "device", dev)
	}
	logger.Info("Rewrote Cilium API endpoint for cloud cluster", "host", host, "port", port)
	return manifest
}

// cloudPrivateNIC maps providers to the VM interface carrying the private
// network (used for pod/node traffic) on their default images.
var cloudPrivateNIC = map[v1alpha1.EnvironmentProvider]string{
	v1alpha1.ProviderDO:    "eth1",
	v1alpha1.ProviderAWS:   "ens5",
	v1alpha1.ProviderGKE:   "ens4",
	v1alpha1.ProviderAzure: "eth0",
	v1alpha1.ProviderCivo:  "eth0",
}

// ── Provided clusters: the CNI is the operator's, not ours ──────────────────
//
// In `clusterMode: provided` the platform installs itself onto a cluster it did
// not build. Two shapes reach this point, because `adhar up`'s preflight refuses
// everything else: a cluster already running Cilium, or a cluster with no CNI at
// all.
//
// For the first, reapplying the embedded Cilium manifests would overwrite the
// operator's own Cilium — its version, its IPAM mode, its kubeProxyReplacement
// setting, its Hubble configuration — with whatever this release happens to pin.
// That is a cluster-wide networking change made as a side effect of installing a
// platform, on infrastructure the operator did not hand over. So it is skipped,
// and the existing Cilium is used as the Gateway data path exactly as configured.
//
// For the second there is nothing to preserve, and Cilium installs as usual.

// clusterModeFromSpec reads the cluster mode the CLI recorded at bootstrap.
// Absent ConfigMap or absent key means the default, compute: an unknown mode
// must behave like the mode the platform has always had, not like the one that
// skips installing the CNI.
func (r *AdharPlatformReconciler) clusterModeFromSpec(ctx context.Context) string {
	cm := &corev1.ConfigMap{}
	key := client.ObjectKey{Name: globals.ClusterSpecConfigMapName, Namespace: globals.AdharSystemNamespace}
	if err := r.Get(ctx, key, cm); err != nil {
		return provider.ClusterModeCompute
	}
	mode, err := provider.NormalizeClusterMode(cm.Data[globals.ClusterSpecClusterModeKey])
	if err != nil {
		return provider.ClusterModeCompute
	}
	return mode
}

// foreignCilium returns the existing Cilium DaemonSet this platform did NOT
// install, if there is one.
//
// Ownership is decided by NAMESPACE, because the embedded install manifest
// hardcodes it: the platform's Cilium agent is `cilium` in the platform
// namespace and nowhere else (ADR-0011 — every package installs there). Anybody
// else's Cilium lives where their chart put it, which upstream means
// kube-system. The alternative signals are worse: the server-side-apply field
// manager is the most precise, but a Cilium installed by one release and then
// adopted by another manager would read as foreign, and a label on the
// DaemonSet is the chart's, not ours to set.
//
// Note what this deliberately does NOT do: it does not try to decide whether a
// Cilium in the platform namespace was installed by THIS platform or an earlier
// one. Both answers lead to the same action — leave it alone on a provided
// cluster, keep reconciling it otherwise.
func (r *AdharPlatformReconciler) foreignCilium(ctx context.Context) (*appsv1.DaemonSet, error) {
	list := &appsv1.DaemonSetList{}
	if err := r.List(ctx, list); err != nil {
		return nil, err
	}
	for i := range list.Items {
		ds := &list.Items[i]
		if ds.Name == "cilium" && ds.Namespace != globals.AdharSystemNamespace {
			return ds, nil
		}
	}
	return nil, nil
}

// ── Host-network Gateway: the edge on a cluster with no load balancer ───────
//
// See provider.GatewayHostNetworkRequired for WHY (Civo compute and the custom
// provider have no cloud-controller-manager, so the Gateway Service never gets
// an address). This is the HOW, and it is three edits that must all land —
// the first attempt at this flipped only the config key, which is why the
// Gateway reported Programmed with node addresses while nothing at all listened
// on 443.

const (
	// The cilium-config key that moves the Gateway's Envoy listeners into the
	// node's own network namespace.
	ciliumHostNetworkKeyOff = `gateway-api-hostnetwork-enabled: "false"`
	ciliumHostNetworkKeyOn  = `gateway-api-hostnetwork-enabled: "true"`

	// cilium-envoy-starter drops capabilities before exec'ing Envoy unless told
	// to keep this one. Without the flag the container has NET_BIND_SERVICE and
	// Envoy still does not.
	ciliumEnvoyStarterArgs = "        args:\n        - '--'\n"
	ciliumEnvoyKeepCapArgs = "        args:\n        - '--keep-cap-net-bind-service'\n        - '--'\n"

	// A port below 1024 in the host network namespace needs NET_BIND_SERVICE.
	ciliumEnvoyCaps = "            add:\n              - NET_ADMIN\n              - SYS_ADMIN\n"
	//nolint:gosec // not a credential: a Linux capability name in a manifest patch
	ciliumEnvoyCapsWithBind = "            add:\n              - NET_ADMIN\n              - SYS_ADMIN\n              - NET_BIND_SERVICE\n"
)

// rewriteCiliumGatewayHostNetwork turns on Cilium's Gateway API host-network
// mode in the embedded install manifest.
//
// Scoped per DOCUMENT rather than applied to the whole file: the capability
// block it patches appears five times in the rendered chart (cilium-agent, two
// init containers, the operator, Envoy) and only Envoy's may change. Returns
// the manifest unchanged, and false, if any of the three anchors is missing —
// a partial rewrite is the failure mode this whole function exists to avoid, so
// it is better to apply the stock manifest and say so.
func rewriteCiliumGatewayHostNetwork(manifest []byte) ([]byte, bool) {
	const separator = "\n---\n"
	docs := strings.Split(string(manifest), separator)

	configPatched, envoyPatched := false, false
	for i, doc := range docs {
		switch {
		case strings.Contains(doc, "name: cilium-config") && strings.Contains(doc, "kind: ConfigMap"):
			if strings.Contains(doc, ciliumHostNetworkKeyOn) {
				configPatched = true // already on
				continue
			}
			if !strings.Contains(doc, ciliumHostNetworkKeyOff) {
				continue
			}
			docs[i] = strings.Replace(doc, ciliumHostNetworkKeyOff, ciliumHostNetworkKeyOn, 1)
			configPatched = true

		case strings.Contains(doc, "k8s-app: cilium-envoy") && strings.Contains(doc, "kind: DaemonSet"):
			patched := doc
			if !strings.Contains(patched, "--keep-cap-net-bind-service") {
				if !strings.Contains(patched, ciliumEnvoyStarterArgs) {
					continue
				}
				patched = strings.Replace(patched, ciliumEnvoyStarterArgs, ciliumEnvoyKeepCapArgs, 1)
			}
			if !strings.Contains(patched, "NET_BIND_SERVICE") {
				if !strings.Contains(patched, ciliumEnvoyCaps) {
					continue
				}
				patched = strings.Replace(patched, ciliumEnvoyCaps, ciliumEnvoyCapsWithBind, 1)
			}
			docs[i] = patched
			envoyPatched = true
		}
	}

	if !configPatched || !envoyPatched {
		return manifest, false
	}
	return []byte(strings.Join(docs, separator)), true
}

// gatewayHostNetworkRequired reports whether THIS cluster needs the
// host-network Gateway, from the provider and cluster mode the CLI recorded at
// bootstrap. Both are in the adhar-cluster-spec ConfigMap because nothing
// inside the cluster can work them out: a kubeadm cluster on Civo instances and
// one on EC2 look identical from here, and only one of them has a load
// balancer.
func (r *AdharPlatformReconciler) gatewayHostNetworkRequired(ctx context.Context) bool {
	cm := &corev1.ConfigMap{}
	key := client.ObjectKey{Name: globals.ClusterSpecConfigMapName, Namespace: globals.AdharSystemNamespace}
	if err := r.Get(ctx, key, cm); err != nil {
		return false
	}
	return provider.GatewayHostNetworkRequired(
		cm.Data["provider"],
		cm.Data[globals.ClusterSpecClusterModeKey],
	)
}
