# Low-Level Design — Secure software supply chain (Chainguard, Sigstore, policy admission)

Detailed design for [ADR-0019](../adr/0019-secure-supply-chain-chainguard.md). This is the authoritative design for the supply-chain contract: the base-image standard, the signing/attestation story, the scanning plane, and the Kyverno admission pack — down to file, chart version, policy name, and enablement state. Status tracking lives in the [Roadmap](../ROADMAP.md) (Phase 3, "Supply-chain policies"). Where a piece is scaffolded but not yet wired for enforcement it is marked 🔜.

## 0. Context recap

An IDP is a supply-chain amplifier: the base images, signing posture, and admission gates it bakes into golden paths get inherited by every team. ADR-0019 defines a **supply-chain contract** with four moving parts: (1) Chainguard/Wolfi base images by default, (2) keyless Cosign signing + SBOM/provenance attestation in CI, (3) Harbor as the scanning distribution choke point (Trivy on push), and (4) Kyverno `verifyImages` admission that requires signatures for workload namespaces, rolled out `Audit → Enforce` per environment. This document describes what actually ships in `platform/stack/packages/security/{cosign,trivy,kyverno-policies,policy-packs}/` and `application/harbor/`, and what remains design-only.

**As-built at a glance — the enablement matrix is the load-bearing fact:**

| Package | Chart / version | local appset | production appset | Role in the contract |
|---|---|---|---|---|
| `security/kyverno-policies` | `kyverno/kyverno-policies` 3.8.0 + `supply-chain.yaml` | `enabled: "false"` | `enabled: "true"` | Admission-time verify (Audit) — **the enforcement half that ships** |
| `security/trivy` | `aqua/trivy-operator` 0.33.1 | `enabled: "false"` | `enabled: "true"` | Continuous in-cluster scan → CRD reports |
| `application/harbor` | Harbor chart | `enabled: "false"` | `enabled: "true"` | Registry choke point; Trivy scan-on-push |
| `security/cosign` | `sigstore/policy-controller` 0.10.6 (app 0.13.1) | `enabled: "false"` | `enabled: "false"` 🔜 | Sigstore `ClusterImagePolicy` admission — **shipped but disabled everywhere** |
| `security/policy-packs` | in-repo `cis.yaml`+`soc2.yaml` | `enabled: "false"` | `enabled: "false"` | Opt-in CIS/SOC2 profiles, all Audit |

All packages are wired into the ApplicationSet list generators and gated by the `enabled` selector (ADR-0004 model) — flipping supply-chain posture is a one-line Git edit, not a redeploy.

## 1. Base-image standard (Chainguard/Wolfi)

The contract's rule is *no shells, no package managers, no unused packages in production images*. Two concrete manifestations exist today:

- The platform's own binary ships on `distroless/static:nonroot` (Dockerfile / GoReleaser), satisfying the rule without Chainguard.
- `cgr.dev/*` is an **allowed registry** in the admission allowlist (§4.3, `restrict-image-registries`), so teams that base on Chainguard Images pull cleanly through policy.

🔜 The golden-path side — Buildpacks run images and `application/adhar-templates` `microservice`/`frontend` scaffolds actually *basing* on `cgr.dev` runtimes with apko/melange for custom bases — is ADR-0018 pipeline-catalog territory and not yet in-repo (the shipped `microservice` template documents a distroless image; ROADMAP Phase 3, "Golden paths"). The admission allowlist already accommodates it, so the switch is a template change, not a policy change.

## 2. Signing & attestation

ADR-0019 specifies **Cosign keyless** (Fulcio short-lived certs bound to the CI OIDC identity, Rekor transparency log) plus SBOM (SPDX) and SLSA-L2+ provenance attestations, produced by ADR-0018's Jenkins X / Tekton pipelines.

### 2.1 Verification path that ships — Kyverno `verifyImages`

The admission-time verifier that is *enabled* in production is not the Sigstore policy-controller — it is the `verify-image-signatures` ClusterPolicy in `security/kyverno-policies/manifests/supply-chain.yaml`:

```yaml
apiVersion: kyverno.io/v1
kind: ClusterPolicy
metadata: { name: verify-image-signatures }
spec:
  validationFailureAction: Audit      # staged: Audit -> Enforce (prod workload ns) — ADR-0019
  background: false
  webhookTimeoutSeconds: 15
  failurePolicy: Ignore               # fail-open: a flapping kyverno must not wedge the apiserver (ADR-0012)
  rules:
    - name: verify-platform-built-images
      match:  { any: [ { resources: { kinds: ["Pod"] } } ] }
      exclude:{ any: [ { resources: { namespaces: [adhar-system, kube-system, crossplane-system] } } ] }
      verifyImages:
        - imageReferences: ["harbor.*/library/*", "ghcr.io/adhar-io/*"]
          attestors:
            - entries:
                - keyless:
                    subject: "https://lighthouse.*"                 # Jenkins X Lighthouse CI identity (ADR-0018)
                    issuer:  "https://keycloak.*/realms/adhar"      # platform OIDC issuer (ADR-0008)
                    rekor: { url: https://rekor.sigstore.dev }
          mutateDigest: true
          verifyDigest: false
          required: false             # advisory in Audit; flip to true when enforcing
```

The Fulcio subject/issuer pattern binds signatures to the **platform's own CI OIDC identity** (Lighthouse authenticated against the `adhar` Keycloak realm) — no long-lived keys. `mutateDigest: true` pins the tag to the resolved digest on admit; `required: false` keeps missing signatures advisory while in Audit.

Only images built by the platform (`harbor.*/library/*`, `ghcr.io/adhar-io/*`) are matched; third-party images pass this rule and are governed by the registry allowlist instead. Platform namespaces are excluded because foundation images are pinned at the release boundary and an admission gate on the platform's own control loop is an ADR-0012-class cluster-wide risk.

### 2.2 Sigstore policy-controller (`security/cosign`) — shipped, disabled

The `cosign` package renders `sigstore/policy-controller` 0.10.6 (`--include-crds`) into `adhar-system`: the `ClusterImagePolicy`/`TrustRoot` CRDs (`policy.sigstore.dev`), the validating webhook (`policy-controller-webhook`, PDB `minAvailable: 1`, `webhook-certs` Secret), and the `config-sigstore-keys` / `config-image-policies` ConfigMaps (shipped with only `_example` data — no live `ClusterImagePolicy` is defined). This is the native cosign admission path (air-gap-capable via `TrustRoot` for a private Sigstore).

It is `enabled: "false"` in **both** local and production. Reason (production appset inline comment / CONFLICTS): `Secret/webhook-certs` and the webhook name collide with Tekton's cosign usage; the package needs an own-namespace re-render before it can co-exist. Until then, Kyverno `verifyImages` (§2.1) is the single admission-time signature verifier.

## 3. Scanning plane — Trivy + Harbor

### 3.1 In-cluster: `security/trivy`

`aqua/trivy-operator` 0.33.1 in `adhar-system`. It continuously scans running workloads and writes findings to CRDs rather than a UI — `vulnerabilityreports`, `sbomreports` / `clustersbomreports`, `exposedsecretreports`, `configauditreports`, `clustercompliancereports`, `rbacassessmentreports`, `infraassessmentreports` (`*.aquasecurity.github.io`, 12 CRDs total). `values.yaml` pins `trivyOperator.scanJobsConcurrentLimit: 5`, `operator.scannerReportTTL: 24h`, and `operator.namespace: adhar-system` explicitly (a stale `trivy-system` env value once crashlooped the operator on the leader-election lease). The `sbomreports` CRD is the in-cluster half of ADR-0019's "SBOM inventory is queryable" requirement.

Enabled in production; disabled locally (extra scan-job churn on a single Kind node).

### 3.2 Registry: `application/harbor`

Harbor is the distribution choke point (proxy-cache upstreams + `library` project for platform builds) and scans **on push** with its embedded Trivy. `values.yaml` keeps `trivy.enabled: false` locally (heaviest optional component — extra pod, large PVC, DB pulls) and enables it on real clusters; `metrics.enabled: true` wires the exporter to kube-prometheus (dashboard gnetId 14075). SBOM attestations land in Harbor alongside images so "which running image ships package X@Y" is a query. Enabled in production, disabled locally.

## 4. Kyverno policy pack (`security/kyverno-policies`)

Generated by `generate-manifests.sh` from `kyverno/kyverno-policies` 3.8.0 (baseline PSS `restricted`, `validationFailureAction: Audit`, `failurePolicy: Ignore`, `replicaCount: 1`, `crds.enabled=true`) plus the hand-written `supply-chain.yaml` and a set of per-component `PolicyException`s under `manifests/exceptions/`.

### 4.1 `supply-chain.yaml` — three ClusterPolicies (all Audit)

| Policy | Rule | Behaviour |
|---|---|---|
| `verify-image-signatures` | §2.1 `verifyImages` keyless | Platform-built images must carry a Sigstore keyless signature from the Lighthouse/Keycloak identity |
| `disallow-latest-tag` | `require-image-tag` foreach | Denies `*:latest` or untagged images (unreproducible); excludes `kube-system` |
| `restrict-image-registries` | `allowed-registries` foreach | Image must match the platform allowlist |

### 4.2 `disallow-latest-tag`

`foreach` over `spec.containers` denying `element.image == "*:latest"` OR `contains(image, ':') == false`. `background: true`, `kube-system` excluded.

### 4.3 `restrict-image-registries` — the allowlist

`foreach` deny when the image matches **none** of: `harbor.*`, `ghcr.io/adhar-io/*`, `cgr.dev/*` (Chainguard — the base-image standard), `registry.k8s.io/*`, `quay.io/*`, `docker.io/*`, `ghcr.io/*`. Excludes `adhar-system`, `kube-system`, `crossplane-system`.

### 4.4 Exceptions (`manifests/exceptions/*.yaml`)

`kyverno.io/v2beta1` `PolicyException` objects scope the **baseline PSS** policies (not the supply-chain ones) away from platform components that legitimately need host paths / privilege / non-`nonroot` users: `kind.yaml` (kube-system, local-path-storage, coredns, etcd…), `argocd.yaml`, `console.yaml`, `crossplane.yaml`, `ingress-nginx.yaml`. Each lists explicit `policyName`+`ruleNames` (including `autogen-*` variants) and a namespace/name match — the surgical alternative to whole-namespace exclusion.

## 5. Compliance profiles (`security/policy-packs`) 🔜

Two opt-in profiles layered on the baseline, `enabled: "false"` everywhere:

- `cis.yaml` — 9 `cis-*` ClusterPolicies (`cis-disallow-privileged-containers`, `cis-disallow-host-namespaces`, `cis-disallow-host-path`, `cis-require-run-as-nonroot`, `cis-disallow-privilege-escalation`, `cis-require-drop-all-capabilities`, `cis-require-resource-requests-limits`, `cis-disallow-default-namespace`, `cis-require-seccomp-runtimedefault`).
- `soc2.yaml` — 5 `soc2-*` ClusterPolicies (`soc2-require-pinned-images`, `soc2-require-workload-labels`, `soc2-restrict-image-registries`, `soc2-require-pod-probes`, `soc2-audit-networkpolicy-presence`).

Every policy is `validationFailureAction: Audit`, labeled `adhar.io/policy-pack: {cis|soc2}` and `app.kubernetes.io/part-of: policy-packs` (so PolicyReports filter per profile), name-prefixed to coexist with the baseline's cluster-scoped policies, and excludes platform namespaces.

## 6. Runtime layer 🔜

ADR-0019's "runtime closes the loop" — a *legitimately signed* image doing illegitimate things — is covered by catalog packages `security/falco` and `security/tetragon`, both present but `enabled: "false"` in every appset and not yet wired to the signing pipeline. Design-only for now.

## 7. Staged rollout — Audit → Enforce

The rollout mirrors the ADR-0004 enabled-gating model, at two granularities:

1. **Package enablement** (per environment appset): local enables none of the supply-chain packages; production enables `trivy`, `kyverno-policies`, `harbor` (not `cosign`, not `policy-packs`).
2. **Policy action** (per ClusterPolicy): **every policy in-repo is `Audit`** — findings surface in `PolicyReport`/`ClusterPolicyReport` and Trivy CRDs, nothing is blocked. The ADR's target end state is `Enforce` in production **workload** namespaces (prod first, local stays Audit), flipping `validationFailureAction: Enforce` and `required: true` once golden-path pipelines sign-and-attest by default. That flip is ROADMAP Phase 3 and has **not** happened in any environment.

Ordering/idempotency: policies are plain ArgoCD-synced manifests (SSA, self-heal). `failurePolicy: Ignore` on every webhook keeps the admission path fail-open so a restarting Kyverno on a single node cannot wedge the apiserver (ADR-0012 webhook hygiene). Platform namespaces are uniformly excluded so the platform's own reconcile loop is never gated.

## 8. Failure modes

- **Kyverno unavailable** → webhooks fail-open (`Ignore`); admission proceeds unverified. Acceptable in Audit; when enforcing, this is the availability/security trade-off the ADR flags.
- **Rekor unreachable** (`verifyImages` keyless) → in Audit with `required: false` the check is advisory and does not fail admission; air-gapped/enforce deployments must run a private Sigstore or fall back to key-based signing (ADR-0019 consequence; `cosign` package's `TrustRoot` CRD is the air-gap hook).
- **Cosign package namespace collision** → the reason the native policy-controller path stays disabled; documented in the production appset comment and ROADMAP.
- **Trivy scan-job pressure** → `scanJobsConcurrentLimit: 5` + local disablement bound the churn.

## 9. Testing

- **Kyverno `chainsaw`/`kyverno-test`** (to add, per exemplar 0023 §10): assert `disallow-latest-tag` denies `nginx:latest` in a workload namespace and admits a pinned digest; `restrict-image-registries` denies `some.random.io/x` and admits `cgr.dev/chainguard/static`; `verify-image-signatures` reports an unsigned `harbor.*/library/*` image in Audit. No such policy test exists in-repo today (`grep` of `platform/controllers/**/*_test.go` and `tests/` finds none) — a gap to close before flipping to Enforce.
- **e2e** (`tests/e2e/bootstrap`): the production-profile assertions should verify Trivy CRDs are established and the three supply-chain ClusterPolicies exist in Audit after sync.
- **Manual verification**: `kubectl get clusterpolicyreport` / `kubectl get vulnerabilityreports -A` surface Audit findings; `kubectl get cip` (once `cosign` is enabled) lists ClusterImagePolicies.

## 10. Code & file map

| Path | Responsibility |
|---|---|
| `platform/stack/packages/security/kyverno-policies/manifests/supply-chain.yaml` | The 3 supply-chain ClusterPolicies (verify-image-signatures, disallow-latest-tag, restrict-image-registries) — **the shipped enforcement half** |
| `platform/stack/packages/security/kyverno-policies/manifests/install.yaml` | Baseline PSS `restricted` policies (chart 3.8.0) |
| `platform/stack/packages/security/kyverno-policies/manifests/exceptions/{kind,argocd,console,crossplane,ingress-nginx}.yaml` | Per-component `PolicyException`s for baseline PSS |
| `platform/stack/packages/security/kyverno-policies/{values.yaml,generate-manifests.sh}` | Audit + fail-open config; chart render script |
| `platform/stack/packages/security/cosign/manifests/install.yaml` | Sigstore policy-controller 0.10.6 (CIP/TrustRoot CRDs + webhook) — disabled |
| `platform/stack/packages/security/cosign/{values.yaml,generate-manifests.sh}` | policy-controller render |
| `platform/stack/packages/security/trivy/manifests/install.yaml` | trivy-operator 0.33.1 + 12 report CRDs |
| `platform/stack/packages/security/trivy/{values.yaml,generate-manifests.sh}` | scan limits, TTL, namespace pin |
| `platform/stack/packages/security/policy-packs/manifests/{cis,soc2}.yaml` | Opt-in CIS (9) / SOC2 (5) profiles, Audit 🔜 |
| `platform/stack/packages/application/harbor/values.yaml` | Registry choke point; `trivy.enabled` per environment |
| `platform/stack/adhar-appset-local.yaml` | local enablement (all supply-chain packages off) |
| `platform/stack/adhar-appset-production.yaml` | production enablement (trivy/kyverno-policies/harbor on; cosign/policy-packs off) |
| `docs/PRODUCTION.md`, `docs/ROADMAP.md` (Phase 3) | Enforcement checklist + status |

## 11. Milestones

- **M1 — Audit everywhere (done)**: kyverno-policies + supply-chain.yaml + trivy + harbor shipped; production enables the scanning/audit plane.
- **M2 — Native signing verification**: re-render `cosign` into its own namespace (resolve the `webhook-certs`/Tekton collision), add a live `ClusterImagePolicy`, decide policy-controller vs Kyverno `verifyImages` as the canonical verifier.
- **M3 — Golden-path base images**: `adhar-templates` + Buildpacks run images base on `cgr.dev`; pipelines sign keyless + emit SBOM/provenance (ADR-0018).
- **M4 — Enforce**: flip `verify-image-signatures`/registry/latest policies to `Enforce` + `required: true` in production workload namespaces; add chainsaw policy tests as a gate; enable `policy-packs` for regulated environments.
- **M5 — Runtime**: enable Falco/Tetragon and wire runtime findings alongside the SBOM inventory.

## 12. Risks

- **Two verifiers, one enabled**: Kyverno `verifyImages` and Sigstore policy-controller both exist; drift risk until M2 picks one. Documented, not resolved.
- **Enforce without signing pipelines**: flipping to Enforce before ADR-0018 pipelines sign-by-default would block every workload — M3 must precede M4.
- **Fail-open vs security**: `failurePolicy: Ignore` trades enforcement guarantees for cluster availability on constrained nodes; revisit for HA production (ADR-0012).
- **Sigstore dependency**: keyless verification needs Fulcio/Rekor; air-gap requires a private Sigstore (`TrustRoot`) or key-based fallback.
- **Allowlist breadth**: `restrict-image-registries` currently permits `docker.io/*`/`ghcr.io/*`/`quay.io/*` — appropriate for Audit, must tighten before Enforce or it verifies nothing meaningful.
</content>
</invoke>
