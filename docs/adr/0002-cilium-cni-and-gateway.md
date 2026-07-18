# ADR-0002: Cilium as CNI, kube-proxy replacement, and Gateway API implementation

**Status**: Accepted · **Date**: 2026-07

## Context

The platform needs container networking, service load-balancing, network policy, network observability, and north-south HTTP routing. The conventional stack composes 3–4 components (e.g. Calico + kube-proxy + NGINX Ingress + a flow tool), each with its own lifecycle, config language, and failure modes. Ingress-NGINX also ties routing to the legacy Ingress API rather than Gateway API.

## Decision

Use **Cilium for all of it**:

- CNI with eBPF data path, **kube-proxy replacement** enabled (Kind config disables default CNI and kube-proxy)
- **Cilium Gateway API** implementation for north-south traffic: GatewayClass `adhar`, shared Gateway `adhar-gateway`, per-package `HTTPRoute`s, TLS terminated at the Gateway (Envoy)
- **Hubble** for flow-level observability
- Cilium network policies for zero-trust microsegmentation; WireGuard for node-to-node encryption in production

The controller pins the Gateway Service's generated node ports (30080/30443) so Kind's static host-port mapping survives reconciliation.

## Consequences

- ✅ One component, one lifecycle for the entire network layer; fewer moving parts in bootstrap
- ✅ Gateway API is the Kubernetes-standard routing seam — routes are portable to any conformant implementation if Cilium is ever replaced
- ✅ eBPF avoids iptables scaling limits; Hubble gives audit-grade flow evidence for free
- ✅ Forward path to service-mesh capabilities (mTLS/SPIFFE, cluster mesh) without adding a sidecar mesh
- ⚠️ Cilium must install before anything schedules — it is the hardest bootstrap dependency and pins the install order
- ⚠️ eBPF requires reasonably modern kernels; constrains exotic on-prem hosts
- ⚠️ HTTPRoutes only resolve after the Gateway is `Programmed`; the controller must sequence and verify this
