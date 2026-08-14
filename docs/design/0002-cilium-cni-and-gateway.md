# Low-Level Design — Cilium as CNI, kube-proxy replacement, and Gateway API

Detailed design for [ADR-0002](../adr/0002-cilium-cni-and-gateway.md). This is the authoritative as-built description of the platform network layer: one component (Cilium 1.20.0) supplies the CNI data path, the kube-proxy replacement, north-south routing through the Cilium Gateway API, and flow observability via Hubble. It maps the real embedded manifests, the Helm value decisions baked into them, the two `AdharPlatform` reconcilers that install them, the node-port pinning that makes Kind's static host mapping survive reconciliation, and the per-package `HTTPRoute` seam.

## 0. Context recap

The conventional stack composes CNI + kube-proxy + an Ingress controller + a flow tool, each with its own lifecycle. ADR-0002 collapses all of it into **Cilium**: eBPF data path with `kubeProxyReplacement` on, the **Cilium Gateway API** implementation (`GatewayClass adhar`, one shared `adhar-gateway`, per-package `HTTPRoute`s, TLS terminated at Envoy), and **Hubble** for flows. Cilium is the hardest bootstrap dependency — nothing else can schedule until it is up — so it pins the foundation install order and the controller must sequence the Gateway after Cilium is programmed.

## 1. Invariants

- **INV-1** Kind creates the node with **no CNI and no kube-proxy** (`disableDefaultCNI: true`, `kubeProxyMode: none`); Cilium must supply both or the cluster never becomes ready.
- **INV-2** All network manifests are embedded (`//go:embed`) and Server-Side Applied — the network layer is offline-capable, no Helm at runtime.
- **INV-3** Install order is fixed: **Gateway API CRDs → Cilium → Gateway**. The CRDs must exist before Cilium starts with Gateway API enabled; the Gateway is created only after Cilium can program it.
- **INV-4** The generated gateway Service's node ports are **pinned** (30080/30443, plus the 8443 listener at 8443) so Kind's static host port-mapping keeps routing across reconciles.
- **INV-5** Routing is expressed only through Gateway API `HTTPRoute`s attached to `adhar-gateway`; packages never own an Ingress controller.

## 2. Manifest provenance (`hack/cilium/`)

The embedded install manifest is generated, not hand-written. [`hack/cilium/generate-manifests.sh`](../../hack/cilium/generate-manifests.sh) Helm-templates the upstream chart into the embedded file:

```bash
CILIUM_VERSION="1.20.0"; CILIUM_NAMESPACE="adhar-system"
helm template cilium cilium/cilium --namespace $CILIUM_NAMESPACE \
  --version "$CILIUM_VERSION" --include-crds -f hack/cilium/values.yaml \
  > platform/controllers/adharplatform/resources/cilium/install.yaml
```

The result is the ~108 KB [`resources/cilium/install.yaml`](../../platform/controllers/adharplatform/resources/cilium/install.yaml) (Cilium CRDs + agent DaemonSet + operator + Hubble + Envoy). The decisive [`values.yaml`](../../hack/cilium/values.yaml) settings that realise ADR-0002:

| Value | Setting | Why |
|---|---|---|
| `kubeProxyReplacement` | `"true"` | Cilium replaces kube-proxy (Kind runs `kubeProxyMode: none`) |
| `k8sServiceHost` / `k8sServicePort` | `"adhar-control-plane"` / `"6443"` | Agents run host-network + no service VIP, so they need a direct API endpoint; the Kind node name is baked in and rewritten per-provider (§5) |
| `gatewayAPI.enabled` | `true` | Turns on the Gateway controller + Envoy config; secrets synced to `cilium-secrets` |
| `l7Proxy` | `true` | Envoy L7 data path for Gateway API / L7 policy |
| `ipam.mode` | `kubernetes`, clusterPool `10.0.0.0/8` /24 | Pod CIDR delegation |
| `routingMode` / `tunnelProtocol` | `""` (defaults) | Tunnel (VXLAN) routing; overridden to explicit `tunnel` + pinned device on cloud (§5) |
| `nodePort.range` | `"8443,32767"` | Widened low bound so the Gateway's 8443 NodePort is allocatable |
| `hubble.enabled` / `hubble.relay.enabled` / `hubble.ui.enabled` | `true` | Flow observability + Relay + UI |
| `encryption.enabled` | `false` | Node-to-node encryption **not** enabled as-built (see Drift) |

## 3. Reconcile flow (`platform/controllers/adharplatform/`)

`installCorePackagesSync` in [`controller.go`](../../platform/controllers/adharplatform/controller.go) drives the ordered installer list; the network layer occupies the first three slots:

```go
installers := []namedInstaller{
    {v1alpha1.GatewayAPICRDsPackageName, r.ReconcileGatewayAPICRDs}, // "gateway-api-crds"
    {v1alpha1.CiliumPackageName,         r.ReconcileCilium},         // "cilium"
    {v1alpha1.GatewayPackageName,        r.ReconcileGateway},        // "gateway"
}
// … then [CNPG if HA] → ArgoCD → Gitea → Crossplane
```

The outer reconcile keeps re-running this block until `Status.Gateway.Available` (among the other core gates) is true (`controller.go:163`). Each sub-reconciler is idempotent SSA via `applyManifest` (`FieldManager = "adhar"`, `ForceOwnership`).

### 3.1 `ReconcileGatewayAPICRDs` ([`gateway.go`](../../platform/controllers/adharplatform/gateway.go))

Applies the single embedded [`resources/gateway-api/crds.yaml`](../../platform/controllers/adharplatform/resources/gateway-api/) (~1.4 MB, the full upstream Gateway API bundle). This precedes Cilium so the agent's `gatewayAPI.enabled: true` finds its CRDs registered.

### 3.2 `ReconcileCilium` ([`cilium.go`](../../platform/controllers/adharplatform/cilium.go))

1. Read `resources/cilium/install.yaml`.
2. `rewriteCiliumAPIEndpoint` (§5) — no-op on Kind, substitutes the real API host and pins the NIC on cloud.
3. `applyManifest(... "Cilium install")`.
4. Apply `resources/cilium/post-install.yaml` — currently only a comment block (Hubble UI is a SPA with a hardcoded `<base href="/">`, so it cannot be served under a sub-path; access is `kubectl port-forward -n adhar-system svc/hubble-ui 12000:80`, or the `hubble.adhar.localtest.me` `HTTPRoute` shipped by the observability package).

`RawCiliumInstallResources` exposes the same bytes for the GitOps/day-2 path.

### 3.3 `ReconcileGateway` ([`gateway.go`](../../platform/controllers/adharplatform/gateway.go))

Provider-branched (`isKind := Provider == ProviderKind || Provider == ""`):

- **Kind** → apply `resources/gateway/gateway.yaml`, then `pinGatewayNodePorts` (§4).
- **Cloud/on-prem** → template `resources/gateway/gateway-cloud.yaml` with `r.Config` (injects the wildcard host), apply, skip pinning.

On success it sets `resource.Status.Gateway.Available = true`, which flips `ConditionGatewayReady` in [`conditions.go`](../../platform/controllers/adharplatform/conditions.go) (`"Cilium Gateway is not programmed yet"` → `Ready`) and contributes to the aggregate `Ready`.

## 4. Gateway resources and node-port pinning

[`resources/gateway/gateway.yaml`](../../platform/controllers/adharplatform/resources/gateway/gateway.yaml) (Kind variant) defines three objects in `adhar-system`:

- **`CiliumGatewayClassConfig` `adhar-gateway-config`** — `service.type: NodePort`, `externalTrafficPolicy: Cluster`. NodePort (not the default LoadBalancer) so it works on Kind without an LB.
- **`GatewayClass` `adhar`** — `controllerName: io.cilium/gateway-controller`, `parametersRef` → the config above.
- **`Gateway` `adhar-gateway`** — three listeners, all `allowedRoutes.namespaces.from: All`:
  - `http` / port 80
  - `https` / port 443, `mode: Terminate`, `certificateRefs: [adhar-cert]`
  - `https-8443` / port 8443 — same cert. In-cluster clients (e.g. ArgoCD doing OIDC discovery against `https://keycloak.adhar.localtest.me:8443/...`) hit the gateway Service on :8443 directly, so the Gateway must also listen there. HTTPRoutes attach to every listener, so each route serves on 80, 443 and 8443 alike.

The TLS secret is `adhar-cert` (`globals.SelfSignedCertSecretName`), the self-signed platform certificate created by the Kind provider.

### 4.1 `pinGatewayNodePorts`

Cilium creates the Service `cilium-gateway-adhar-gateway` (`cilium-gateway-<gateway-name>`) asynchronously after accepting the Gateway. `CiliumGatewayClassConfig` can select NodePort type but **cannot pin port numbers**, so the controller patches them to the fixed values Kind maps host ports to:

```go
const (
    gatewayServiceName      = "cilium-gateway-adhar-gateway"
    gatewayHTTPNodePort     = 30080 // host 8080 -> 30080 -> :80
    gatewayHTTPSNodePort    = 30443 // host 8443 -> 30443 -> :443
    gatewayAltHTTPSPort     = 8443  // listener port
    gatewayAltHTTPSNodePort = 8443  // node port == listener port (loopback issuer)
)
```

`pinGatewayNodePorts` loops up to 18×5s (~90 s): waits for the Service to exist as `NodePort`, and if `gatewayNodePortsPinned` is false, uses `retry.RetryOnConflict` (Cilium re-reconciles this Service, so a plain `Update` can 409) to run `setGatewayNodePorts`, mapping port 80→30080, 443→30443, 8443→8443.

This step is **intentionally non-fatal**: on a cold cluster Cilium may need more than one reconcile pass to program the Gateway and create the Service. If pinning hasn't converged, `ReconcileGateway` returns `nil` **without** setting `Status.Gateway.Available`, so the core-install gate re-runs the reconciler on the next pass while ArgoCD/Gitea/Crossplane still proceed. It must never abort the rest of the foundation.

### 4.2 Kind host mapping (`platform/providers/kind/resources/kind.yaml.tmpl`)

The pinned ports line up with the node's `extraPortMappings` and the widened NodePort range:

```yaml
extraPortMappings:
  - { containerPort: 30080, hostPort: {{ .HTTPPort | default 80 }} }
  - { containerPort: 30443, hostPort: {{ .HTTPSPort | default 443 }} }
  - { containerPort: 32222, hostPort: 32222 }   # Gitea SSH
kubeadmConfigPatches:                            # apiServer extraArgs
  service-node-port-range: "8443-32767"
networking:
  disableDefaultCNI: true   # Cilium is the CNI
  kubeProxyMode: none       # Cilium replaces kube-proxy
```

### 4.3 Cloud/production edge variant

[`resources/gateway/gateway-cloud.yaml`](../../platform/controllers/adharplatform/resources/gateway/gateway-cloud.yaml) (selected for non-Kind, rendered with the platform host — marked roadmap P1.3): `service.type: LoadBalancer` (no pinning — LB port allocation would never converge under `retry`), the `https` listener carries `hostname: "*.{{ .Host }}"` and a `cert-manager.io/cluster-issuer: adhar-selfsigned` annotation so cert-manager's Gateway support manages the wildcard cert, HTTP stays open for ACME HTTP-01 / external-dns, and there is **no** 8443 listener (that exists only for Kind's loopback mapping).

## 5. Cloud API endpoint + NIC rewrite (`rewriteCiliumAPIEndpoint`)

Because agents run host-network with the kube-proxy replacement, they cannot reach the API through the in-cluster service VIP. On Kind the baked-in `adhar-control-plane` node name is directly reachable and left untouched. On a cloud provider the controller rewrites `value: "adhar-control-plane"` → the parsed API host from `ctrlconfig.GetConfig()` (default port 6443).

Multi-NIC cloud VMs also defeat Cilium's device auto-detection (it derives the direct-routing device from the node InternalIP, which external-cloud-provider nodes lack until the CCM initialises them — after Cilium). To break the cycle, `cloudPrivateNIC` pins the private interface per provider, injecting `direct-routing-device` + `devices` under `routing-mode: tunnel`:

```go
var cloudPrivateNIC = map[v1alpha1.EnvironmentProvider]string{
    ProviderDO: "eth1", ProviderAWS: "ens5", ProviderGKE: "ens4",
    ProviderAzure: "eth0", ProviderCivo: "eth0",
}
```

## 6. The routing seam — per-package `HTTPRoute`

Packages attach to the shared Gateway rather than owning a controller. 45 `HTTPRoute`s across `platform/stack/packages/**` follow one shape (e.g. [adhar-console](../../platform/stack/packages/core/adhar-console/manifests/httproute.yaml)):

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: { name: console, namespace: adhar-system }
spec:
  parentRefs:
    - { name: adhar-gateway, namespace: adhar-system }
  hostnames: [ console.adhar.localtest.me ]
  rules:
    - matches: [ { path: { type: PathPrefix, value: / } } ]
      backendRefs: [ { name: console, port: 80 } ]
```

TLS terminates at the Gateway (`adhar-cert`); backends serve plain HTTP. Routes needing a path strip use a Gateway API `URLRewrite` filter in place of the old nginx `rewrite-target` (the Hubble UI route replaces the `/hubble` prefix with `/`). ArgoCD `sync-wave` annotations order route creation after the Gateway is programmed.

## 7. Ordering, idempotency, failure modes

- **Ordering** — Gateway API CRDs precede Cilium (agent needs them at startup); the Gateway follows Cilium (needs the controller running to be programmed and to generate the Service); the CNPG operator (HA only) slots after the Gateway and before Gitea.
- **Idempotency** — every apply is SSA with `FieldManager="adhar"`; a partially-failed pass adopts existing objects. `gatewayNodePortsPinned` short-circuits the patch when ports already match.
- **Cold-start convergence** — Gateway programming is eventually-consistent: `ReconcileGateway` leaves `Status.Gateway.Available` unset until the Service is pinned, and the outer loop requeues (the whole foundation is re-driven while HTTPRoutes wait for the Gateway to become `Programmed`).
- **Cloud NIC race** — without the `cloudPrivateNIC` pin, Cilium picks the wrong device before the CCM initialises node IPs, and pod traffic blackholes; the pin makes the choice deterministic.

## 8. Testing

- [`cilium_test.go`](../../platform/controllers/adharplatform/cilium_test.go) — `ReconcileCilium` runs against a fake client; asserts the embedded manifest path resolves.
- [`gateway_test.go`](../../platform/controllers/adharplatform/gateway_test.go) — `ReconcileGatewayAPICRDs` reads and applies the embedded CRD bundle without a "reading … manifest" error (apply errors against the fake client tolerated).
- [`conditions_test.go`](../../platform/controllers/adharplatform/conditions_test.go) — sets `Status.Gateway.Available = true` and asserts `ConditionGatewayReady`/aggregate `Ready` flip.
- [`ha_test.go`](../../platform/controllers/adharplatform/ha_test.go) — installer ordering with the CNPG slot inserted.
- **e2e** ([`tests/e2e/bootstrap`](../../tests/e2e/bootstrap)) — a full `adhar up` exercises the real path: Cilium becomes the CNI on a `disableDefaultCNI`/`kubeProxyMode: none` node, the Gateway Service is pinned to 30080/30443/8443, and platform hosts resolve through it.

## 9. Code & file map

| Path | Responsibility |
|---|---|
| [`hack/cilium/generate-manifests.sh`](../../hack/cilium/generate-manifests.sh) | Helm-templates Cilium 1.20.0 → embedded install.yaml |
| [`hack/cilium/values.yaml`](../../hack/cilium/values.yaml) | kube-proxy replacement, Gateway API, IPAM, Hubble, NodePort range |
| [`resources/cilium/install.yaml`](../../platform/controllers/adharplatform/resources/cilium/install.yaml) | Embedded generated Cilium manifest (CRDs + agent + operator + Hubble + Envoy) |
| [`resources/cilium/post-install.yaml`](../../platform/controllers/adharplatform/resources/cilium/post-install.yaml) | Hubble UI access note |
| [`resources/gateway-api/crds.yaml`](../../platform/controllers/adharplatform/resources/gateway-api/) | Upstream Gateway API CRD bundle |
| [`resources/gateway/gateway.yaml`](../../platform/controllers/adharplatform/resources/gateway/gateway.yaml) | Kind GatewayClass/config/Gateway (NodePort, 3 listeners) |
| [`resources/gateway/gateway-cloud.yaml`](../../platform/controllers/adharplatform/resources/gateway/gateway-cloud.yaml) | Cloud edge variant (LoadBalancer + cert-manager) |
| [`cilium.go`](../../platform/controllers/adharplatform/cilium.go) | `ReconcileCilium`, `rewriteCiliumAPIEndpoint`, `cloudPrivateNIC` |
| [`gateway.go`](../../platform/controllers/adharplatform/gateway.go) | `ReconcileGatewayAPICRDs`, `ReconcileGateway`, `pinGatewayNodePorts` |
| [`controller.go`](../../platform/controllers/adharplatform/controller.go) | `installCorePackagesSync` ordering + `Gateway.Available` gate |
| [`conditions.go`](../../platform/controllers/adharplatform/conditions.go) | `ConditionGatewayReady` wiring |
| [`platform/providers/kind/resources/kind.yaml.tmpl`](../../platform/providers/kind/resources/kind.yaml.tmpl) | CNI/kube-proxy off, host port map, NodePort range |
| `platform/stack/packages/**/httproute.yaml` (×45) | Per-package Gateway API routes |

## 10. Drift from ADR-0002

- **WireGuard encryption** — the ADR states "WireGuard for node-to-node encryption in production", but `values.yaml` ships `encryption.enabled: false` (type `ipsec` default, unused). Node-to-node encryption is **not yet wired**; it is a values flip when the production edge (P1.3) lands.
- **Zero-trust network policies** — the ADR cites "Cilium network policies for zero-trust microsegmentation". The capability is present (Cilium is the CNI, CRDs installed), but no default-deny `CiliumNetworkPolicy` set is shipped in the bootstrap; microsegmentation is opt-in per package today.
- **Cloud Gateway** — `gateway-cloud.yaml` (LoadBalancer + cert-manager wildcard) is present but annotated **roadmap P1.3**; the fully exercised path is the Kind NodePort variant.
</content>
</invoke>
