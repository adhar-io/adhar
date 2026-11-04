# hubble

Hubble relay and UI ship with the bootstrap Cilium install
(`platform/controllers/adharplatform/resources/cilium/install.yaml`, rendered
from `hack/cilium/values.yaml`). This package adds what sits on top:

| ApplicationSet element | Path | What |
| --- | --- | --- |
| `hubble` | `manifests/` | Hubble UI HTTPRoute + Keycloak oauth2-proxy |
| `hubble-l7-visibility` | `manifests/l7-visibility/` | Observe-only L7 visibility for app namespaces |

## L7 visibility (`hubble-l7-visibility`)

Without an L7 policy Cilium sees only L3/L4, so Hubble flows for team apps have
no HTTP method, path, status or latency, and the console Service map shows edges
without req/s, error rate or p95. `adhar-app-l7-visibility` is a
`CiliumClusterwideNetworkPolicy` that routes app traffic through Cilium's L7
proxy purely so Hubble can record it.

It cannot block traffic:

- `enableDefaultDeny: {ingress: false, egress: false}` keeps the selected pods out
  of default-deny (Cilium 1.15+, `enable-non-default-deny-policies: "true"`).
- Egress rules only. Egress in app namespaces is not restricted, so these allows
  add nothing. An ingress rule would widen the environment's ingress
  default-deny (same namespace + adhar-system + Gateway) to every identity on
  the listed ports.
- All L7 rules are allow-all (`http: [{}]`, `dns: [{matchPattern: "*"}]`).
  Envoy keeps the original source identity, so the destination's ingress
  policy decides exactly as before.

**Scope**: pods in namespaces labelled `adhar.io/plane` = `workload` or
`preview`. Platform namespaces (`adhar-system` is `control`, `kube-system` has no
plane label) are never selected.

**Ports**: TCP 80, 3000, 5000, 8000, 8080, 8081 (HTTP) and 50051 (gRPC), plus
DNS on 53. L7 rules need explicit ports. The proxy expects plaintext HTTP/1.1 or
h2c on these ports, so TLS or another protocol on one of them would fail. With
kube-proxy replacement the matched port is the backend `targetPort`.

**Cost**: each matched request goes through the node's `cilium-envoy` (about
sub-ms to 1-2 ms more at p50, more at the tail, and Envoy CPU/memory that grows
with request rate). App DNS goes through the agent's DNS proxy. Hubble emits
about 2 more events per request. To keep the proxy transparent,
`hack/cilium/values.yaml` sets `envoy.httpRetryCount: 0`, so a failed POST is
never replayed, and `envoy.streamIdleTimeoutDurationSeconds: 3600`, so idle
websockets and SSE are not cut at 5 minutes. One visible difference: a request
the destination's policy denies now gets an HTTP 503 from Envoy instead of a TCP
timeout.

**Switches**

- Cluster-wide: set `enabled: "false"` on the `hubble-l7-visibility` element
  (`platform/stack/adhar-appset-*.yaml` and `environments/*/config.yaml`), then run
  `adhar upgrade --yes`.
- Per environment: add the namespace label `adhar.io/l7-visibility: disabled`
  through the Environment XR (`spec.parameters.labels`). Don't use kubectl for
  this, because provider-kubernetes owns the Namespace.

**Default**: on in production/gitops, off in the local profile, where the
`hubble` package itself is off. It defaults to on because the risks are bounded:
it is egress-only, never denies, and touches only app namespaces. Data-service
ports are excluded, and the latency cost is small for the request rates these
environments see. Switch it off for latency-critical or high-RPS environments.

**Caveat**: Cilium policies are additive. If an egress default-deny is ever
added for app namespaces, scope these rules (`toEndpoints`/`toEntities`) to
match it, or the listed ports become open egress.

## Hubble settings that go with it (bootstrap Cilium, `hack/cilium/values.yaml`)

- `hubble.eventBufferCapacity: 65535` (default 4095). The ring overflowed, and
  the console reported about 900 lost events per window. A full ring costs
  roughly 32-96 MiB more RSS per cilium-agent.
- `hubble.eventQueueSize: 16384`. The monitor-to-Hubble queue, a few MiB at most.
- `hubble.metrics.enabled`: `http` is replaced by
  `httpV2:labelsContext=source_namespace,source_workload,destination_namespace,destination_workload`.
  This gives Prometheus per-edge `hubble_http_requests_total` (with
  `status`/`reporter`) and `hubble_http_request_duration_seconds`. httpV2 has no
  `hubble_http_responses_total`. Cardinality is about one series per (client
  workload, server workload, method, status, reporter) per node, times about
  12 buckets for the histogram. There are no pod or IP labels.

These are cilium-agent start-up options. `adhar upgrade --yes` re-applies the
ConfigMap, but the agents read it only when they start
(`rollOutCiliumPods: false`). Restart them once after the upgrade:
`kubectl -n adhar-system rollout restart ds/cilium`. Leave `cilium-envoy`
alone: retries and idle timeouts reach it over xDS from the agent, and
restarting it drops in-flight Gateway connections.

## Console reachability

The console (adhar-system) dials `hubble-relay.adhar-system.svc:80` (target
4245, plaintext gRPC). No NetworkPolicy or Cilium policy selects either pod, so
the connection is allowed. Don't add a Kubernetes NetworkPolicy that selects
`hubble-relay` "to allow the console": it would put the relay into ingress
default-deny and cut off Hubble UI.
