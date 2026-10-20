# Kyverno half of the supply chain

Applied by the `supply-chain-kyverno` ApplicationSet element, separate from
`supply-chain` so the build path can be enabled without Kyverno (the local
curated core turns Kyverno off). Requires the `kyverno` package:

- `10-kpack-build-ca-trust.yaml` — mutates kpack build pods to trust the
  in-cluster Harbor CA. Without it, builds cannot push to Harbor over TLS.
- `40-verify-images.yaml` — the enforce policy that admits only images signed
  by the platform's cosign key. Without it, unsigned images are admitted.
- `45-verify-scan-attestation.yaml` — AUDIT only: reports workloads whose image
  has no signed Trivy `vuln` attestation, one older than 7 days, or unwaived
  CRITICAL findings in it. Never blocks; the signature policy decides admission.
- `50-scan-governance.yaml` — only security reviewers / platform admins may
  write the `scan-policy` / `scan-waivers` ConfigMaps or approve/reject a scan
  review (`adhar.io/scan-approval` on a PipelineRun); stamps
  `adhar.io/scan-approval-by` / `-at` from the request. Without Kyverno, RBAC
  alone applies — and platform-developer's cluster-wide `edit` can write both.
