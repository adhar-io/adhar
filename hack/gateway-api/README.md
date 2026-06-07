# Gateway API CRDs

The Adhar platform routes traffic through the **Cilium Gateway API** (replacing
ingress-nginx). Cilium's Gateway controller needs the upstream
[Gateway API](https://gateway-api.sigs.k8s.io/) CRDs installed **before** Cilium
starts with `gatewayAPI.enabled: true`.

## Why the experimental channel

Cilium's Gateway controller sets up a field indexer on `TLSRoute` at
`gateway.networking.k8s.io/v1alpha2`. The **standard** channel ships the
`tlsroutes` CRD but only *serves* `v1` (`v1alpha2 served=false`), so cilium-operator
crashes on startup:

```
failed to create gateway controller: ... field indexer "backendServiceTLSRouteIndex":
no matches for kind "TLSRoute" in version "gateway.networking.k8s.io/v1alpha2"
```

The **experimental** channel serves `v1alpha2`, so we vendor that bundle.

## Regenerating

```bash
bash hack/gateway-api/generate-manifests.sh
```

This downloads the pinned `GATEWAY_API_VERSION` (currently `v1.5.1`) experimental
bundle, strips the `safe-upgrades` ValidatingAdmissionPolicy (which would block
applying experimental CRDs over standard ones), and writes
`platform/controllers/adharplatform/resources/gateway-api/crds.yaml` (embedded
into the controller and applied first during `adhar up`).

Keep `GATEWAY_API_VERSION` in sync with the Cilium version in `hack/cilium` —
a Cilium bump may require a newer Gateway API bundle.
