# Kyverno half of the supply chain

Applied by the `supply-chain-kyverno` ApplicationSet element, separate from
`supply-chain` so the build path can be enabled without Kyverno (the local
curated core turns Kyverno off). Requires the `kyverno` package:

- `10-kpack-build-ca-trust.yaml` — mutates kpack build pods to trust the
  in-cluster Harbor CA. Without it, builds cannot push to Harbor over TLS.
- `40-verify-images.yaml` — the enforce policy that admits only images signed
  by the platform's cosign key. Without it, unsigned images are admitted.
