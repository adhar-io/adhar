# Low-Level Design — Secure software supply chain (Chainguard, Sigstore, policy admission)

Detailed design for [ADR-0019](../adr/0019-secure-supply-chain-chainguard.md). This is the authoritative design for the supply-chain contract: the base-image standard, the signing/attestation story, the scanning plane, and the Kyverno admission pack — down to file, chart version, policy name, and enablement state. Status tracking lives in the [Roadmap](../ROADMAP.md) (Phase 3, "Supply-chain policies"). Where a piece is scaffolded but not yet wired for enforcement it is marked 🔜.

**Start at §2.0** — what the platform actually signs with is key-based, not keyless, and every other section follows from that. §7 is the Audit→Enforce switch; §9.1 is the drill that must pass before you use it.

## 0. Context recap

An IDP is a supply-chain amplifier: the base images, signing posture, and admission gates it bakes into golden paths get inherited by every team. ADR-0019 defines a **supply-chain contract** with four moving parts: (1) Chainguard/Wolfi base images by default, (2) keyless Cosign signing + SBOM/provenance attestation in CI, (3) Harbor as the scanning distribution choke point (Trivy on push), and (4) Kyverno `verifyImages` admission that requires signatures for workload namespaces, rolled out `Audit → Enforce` per environment. This document describes what actually ships in `platform/stack/packages/security/{cosign,trivy,kyverno-policies,policy-packs}/` and `application/harbor/`, and what remains design-only.

**As-built at a glance — the enablement matrix is the load-bearing fact:**

| Package | Chart / version | local appset | production appset | Role in the contract |
|---|---|---|---|---|
| `security/supply-chain-policies` → `manifests/audit` | in-repo, 3 ClusterPolicies | `enabled: "true"` | `enabled: "true"` | **Stage 1** — admission-time verify in Audit. Always on |
| `security/supply-chain-policies` → `manifests/enforce` | in-repo, the same 3 policies | `enabled: "false"` | `enabled: "false"` | **Stage 2** — the same rules, `failureAction: Enforce`. **The knob** |
| `security/kyverno-policies` | `kyverno/kyverno-policies` 3.9.0 (+ `namespace-governance.yaml`) | `enabled: "true"` | `enabled: "true"` | Baseline PSS `restricted`, plane-governance (Audit) |
| `application/supply-chain` | in-repo Tekton | `enabled: "true"` | `enabled: "true"` | Build identity + `cosign-key` + the `cosign-sign` Task; also a narrow always-Enforce policy (§2.3) |
| `security/trivy` | `aqua/trivy-operator` 0.33.1 | `enabled: "false"` | `enabled: "false"` | Continuous in-cluster scan → CRD reports |
| `application/harbor` | Harbor chart | `enabled: "true"` | `enabled: "true"` | Registry choke point; Trivy scan-on-push |
| `security/cosign` | `sigstore/policy-controller` 0.10.7 | `enabled: "true"` | `enabled: "true"` | Sigstore `ClusterImagePolicy` admission in `adhar-system` (no live CIP defined) |
| `security/policy-packs` | in-repo `cis.yaml`+`soc2.yaml` | `enabled: "false"` | `enabled: "false"` | Opt-in CIS/SOC2 profiles, all Audit |

All packages are wired into the ApplicationSet list generators and gated by the `enabled` selector (ADR-0004 model) — flipping supply-chain posture is a one-line Git edit, not a redeploy.

## 1. Base-image standard (Chainguard/Wolfi)

The contract's rule is *no shells, no package managers, no unused packages in production images*. Two concrete manifestations exist today:

- The platform's own binary ships on `distroless/static:nonroot` (Dockerfile / GoReleaser), satisfying the rule without Chainguard.
- `cgr.dev/*` is an **allowed registry** in the admission allowlist (§4.3, `restrict-image-registries`), so teams that base on Chainguard Images pull cleanly through policy.

🔜 The golden-path side — Buildpacks run images and `application/adhar-templates` `microservice`/`frontend` scaffolds actually *basing* on `cgr.dev` runtimes with apko/melange for custom bases — is ADR-0018 pipeline-catalog territory and not yet in-repo (the shipped `microservice` template documents a distroless image; ROADMAP Phase 3, "Golden paths"). The admission allowlist already accommodates it, so the switch is a template change, not a policy change.

## 2. Signing & attestation

### 2.0 What the platform actually signs with — read this first

This is the fact every other section depends on, and it is **not** what a casual read of ADR-0019 suggests:

| Where | How | Identity | Transparency log |
|---|---|---|---|
| In-cluster builds (Tekton `app-ci` → Harbor) | **Key-based** | `cosign-key` Secret, `adhar-system` | **None** (`--tlog-upload=false`) |
| Project releases (`ghcr.io/adhar-io/*`) | **Keyless** | GitHub Actions OIDC, `release.yaml` workflow | Rekor (public) |

The in-cluster signer is the `cosign-sign` Task in `application/supply-chain/manifests/60-build-pipelines.yaml`:

```yaml
        - sign
        - --key=k8s://adhar-system/cosign-key
        - --tlog-upload=false
        - --allow-http-registry=true
        - "$(params.image):$(params.tag)"
```

with `COSIGN_PASSWORD: adhar`. So: **key, not keyless, and no Rekor entry.** That is the correct choice — a Kind/kubeadm cluster has no ambient OIDC identity Fulcio would issue a certificate for — but it means a policy that demands a Rekor lookup can never pass.

The key is **not ephemeral**, which was the open question: `application/supply-chain/manifests/00-foundation.yaml` runs a `cosign-keygen` Job as an ArgoCD `Sync` hook whose init container runs `cosign generate-key-pair` and whose main container creates Secret `cosign-key` **only if it does not already exist**:

```bash
if kubectl get secret cosign-key -n adhar-system >/dev/null 2>&1; then
  echo "cosign-key already present — nothing to do"; exit 0
fi
kubectl create secret generic cosign-key -n adhar-system \
  --from-file=cosign.key=/shared/cosign.key \
  --from-file=cosign.pub=/shared/cosign.pub \
  --from-literal=cosign.password=adhar
```

The keypair is therefore generated **once at install** by a hook Job — the same pattern as the platform's other credential jobs — and survives every re-sync, so `cosign.pub` is a stable, platform-owned public key that an admission policy can be pinned to. Rotation means deleting the Secret and letting the Job regenerate; that invalidates every prior signature, so it is a deliberate operation, not a side effect.

Kyverno runs in `adhar-system` and its `kyverno:admission-controller` Role already grants `get` on Secrets there, so the policy reads the public half **live** from the Secret — nothing to copy, nothing to drift.

ADR-0019 also specifies SBOM (SPDX) and SLSA-L2+ provenance attestations. Those are produced for **releases** (`.goreleaser.yaml` `sboms` + `signs`); the in-cluster pipelines do not yet attach attestations, so no `attestations:` block appears in the admission policy. 🔜

### 2.1 Verification path that ships — Kyverno `verifyImages`

The admission-time verifier that is *enabled* is not the Sigstore policy-controller — it is the `verify-image-signatures` ClusterPolicy in `security/supply-chain-policies/manifests/audit/verify-image-signatures.yaml.tmpl` (and its `-enforce` twin under `manifests/enforce/`):

```yaml
spec:
  background: false
  webhookTimeoutSeconds: 15
  failurePolicy: Ignore               # fail-open: a flapping kyverno must not wedge the apiserver (ADR-0012)
  rules:
    - name: verify-platform-built-images
      match:  { any: [ { resources: { kinds: ["Pod"] } } ] }
      exclude:
        any:
          - resources: { namespaces: [adhar-system, kube-system, kube-public, kube-node-lease,
                                      local-path-storage, crossplane-system, kpack-system,
                                      cert-manager, cosign-system] }
          - resources: { namespaceSelector: { matchLabels: { adhar.io/plane: control } } }
      verifyImages:
        - failureAction: Audit          # the ONLY difference in manifests/enforce/ (plus digest pinning)
          imageReferences:
            - "harbor-core.adhar-system.svc.cluster.local/library/*"
            - "harbor.{{ .Host }}*/library/*"     # rendered at seed time; trailing * covers ":8443" or no port
            - "ghcr.io/adhar-io/*"
          imageRegistryCredentials: { allowInsecureRegistry: true }   # Harbor's TLS is the per-cluster adhar CA
          attestors:
            - count: 1                  # ANY ONE entry satisfies the rule
              entries:
                - keys:                 # (a) what the Tekton cosign-sign Task actually produces
                    secret: { name: cosign-key, namespace: adhar-system }
                    rekor: { ignoreTlog: true }   # --tlog-upload=false: there is no Rekor entry
                    ctlog: { ignoreSCT: true }
                - keyless:              # (b) ghcr.io/adhar-io/* release images, .goreleaser docker_signs
                    issuer: "https://token.actions.githubusercontent.com"
                    subjectRegExp: "^https://github\\.com/adhar-io/adhar/\\.github/workflows/release\\.yaml@refs/tags/.*$"
                    rekor: { url: "https://rekor.sigstore.dev" }
          required: true
          mutateDigest: false           # enforce variant: true
          verifyDigest: false           # enforce variant: true
```

Three things changed relative to the policy that shipped before, and all three were latent bugs that only Audit mode hid:

1. **`rekor: {url: https://rekor.sigstore.dev}` on the key attestor → `rekor: {ignoreTlog: true}`.** The pipeline signs with `--tlog-upload=false`. Demanding a transparency-log entry that is never written means **every platform-built image would have failed verification the moment the policy enforced**.
2. **`imageReferences: ["harbor.*/library/*"]` matched nothing.** The pipeline pushes to `harbor-core.adhar-system.svc.cluster.local/library/<repo>` — `harbor.` requires a literal dot, `harbor-core` has a hyphen. The rule silently matched zero images. It now lists the in-cluster service name explicitly and templates the platform-edge name (`harbor.{{ .Host }}*`) at seed time, so it is correct on every topology.
3. **`required: false` → `required: true`.** Audit is only useful if it reports what Enforce would block; with `required: false` an unsigned image was not even a finding.

`count: 1` makes the two attestor entries **alternatives** — the platform key OR the GitHub Actions keyless identity. `mutateDigest`/`verifyDigest` stay false in Audit because Kyverno rejects digest mutation on a non-enforcing rule; the enforce variant turns both on, so what runs is exactly what was verified.

Only images built or released by the platform are matched; third-party images pass this rule and are governed by the registry allowlist instead.

### 2.3 The narrow always-Enforce policy that already existed

`application/supply-chain/manifests/40-verify-images.yaml` ships `verify-supply-chain-images`: **already `Enforce`**, `required: true`, `mutateDigest: true`, scoped to `harbor-core.adhar-system.svc.cluster.local/library/*` and excluding `adhar-system`. Its public key is a throwaway placeholder PEM overwritten at sync time by the `cosign-policy-sync` PostSync Job, which reads `cosign.pub` out of the `cosign-key` Secret and patches it into the policy — and it correctly sets `rekor: {ignoreTlog: true}`.

So enforcement of *platform-built* images in workload namespaces is not new. What this design adds is the **staged, environment-wide** pack around it: the same posture extended to `ghcr.io/adhar-io/*` and the platform-edge Harbor name, plus the tag and registry rules, with an Audit stage that predicts it and an Enforce stage that is a one-line switch. The two are consistent (same key, same `ignoreTlog`), so running both is harmless.

### 2.2 Sigstore policy-controller (`security/cosign`) — shipped, enabled in production

The `cosign` package renders `sigstore/policy-controller` 0.10.6 (`--include-crds`) into `adhar-system` (ADR-0011; it lived in its own `cosign-system` namespace until 2026-09): the `ClusterImagePolicy`/`TrustRoot` CRDs (`policy.sigstore.dev`), the validating webhook (`policy-controller-webhook`, PDB `minAvailable: 1`, `webhook-certs` Secret), and the `config-sigstore-keys` / `config-image-policies` ConfigMaps (shipped with only `_example` data — no live `ClusterImagePolicy` is defined). This is the native cosign admission path (air-gap-capable via `TrustRoot` for a private Sigstore).

It is `enabled: "true"` in production and `enabled: "false"` in the local core. The earlier collision (CONFLICTS.md) is resolved differently now that cosign is back in `adhar-system`: Tekton reads its Secret name from `WEBHOOK_SECRET_NAME` (`tekton-webhook-certs`) and pins `WEBHOOK_PORT`, so cosign's `Service/webhook` service link is harmless; buildpack (kpack), which hardcodes the same `webhook-certs`, keeps `kpack-system`.

## 3. Scanning plane — Trivy + Harbor

### 3.1 In-cluster: `security/trivy`

`aqua/trivy-operator` 0.33.1 in `adhar-system`. It continuously scans running workloads and writes findings to CRDs rather than a UI — `vulnerabilityreports`, `sbomreports` / `clustersbomreports`, `exposedsecretreports`, `configauditreports`, `clustercompliancereports`, `rbacassessmentreports`, `infraassessmentreports` (`*.aquasecurity.github.io`, 12 CRDs total). `values.yaml` pins `trivyOperator.scanJobsConcurrentLimit: 5`, `operator.scannerReportTTL: 24h`, and `operator.namespace: adhar-system` explicitly (a stale `trivy-system` env value once crashlooped the operator on the leader-election lease). The `sbomreports` CRD is the in-cluster half of ADR-0019's "SBOM inventory is queryable" requirement.

Enabled in production; disabled locally (extra scan-job churn on a single Kind node).

### 3.2 Registry: `application/harbor`

Harbor is the distribution choke point (proxy-cache upstreams + `library` project for platform builds) and scans **on push** with its embedded Trivy. `values.yaml` keeps `trivy.enabled: false` locally (heaviest optional component — extra pod, large PVC, DB pulls) and enables it on real clusters; `metrics.enabled: true` wires the exporter to kube-prometheus (dashboard gnetId 14075). SBOM attestations land in Harbor alongside images so "which running image ships package X@Y" is a query. Enabled in production, disabled locally.

## 4. The supply-chain policy pack (`security/supply-chain-policies`)

A package of its own since 2026-09 (it used to be a single `supply-chain.yaml` inside `kyverno-policies`). It contains **no chart** — six hand-written files, the same three policies twice:

```
security/supply-chain-policies/
  adhar-package.yaml
  README.md                              <- the staging recipe, authoritative
  manifests/audit/verify-image-signatures.yaml.tmpl
  manifests/audit/disallow-latest-tag.yaml
  manifests/audit/restrict-image-registries.yaml
  manifests/enforce/verify-image-signatures.yaml.tmpl
  manifests/enforce/disallow-latest-tag.yaml
  manifests/enforce/restrict-image-registries.yaml
```

One policy per file, deliberately: each can be staged to Enforce independently (signatures first, registry allowlist last). `verify-image-signatures` is a `.yaml.tmpl` because it needs the platform host; the other two are plain YAML because they contain Kyverno `{{ }}` JMESPath expressions, which the seed-time Go template engine would try to evaluate.

`security/kyverno-policies` keeps what it always was: the generated `kyverno/kyverno-policies` 3.9.0 baseline PSS `restricted` chart render plus `namespace-governance.yaml` and the per-component `PolicyException`s under `manifests/exceptions/`.

### 4.1 The three policies

| Policy (audit / enforce) | Rule | Behaviour |
|---|---|---|
| `verify-image-signatures` / `…-enforce` | §2.1 `verifyImages`, `count: 1` over [platform key, GH Actions keyless] | Platform-built and Adhar-released images must carry a valid cosign signature |
| `disallow-latest-tag` / `…-enforce` | `require-image-tag` foreach over `containers` **and** `initContainers` | Denies `*:latest` or untagged images (unreproducible) |
| `restrict-image-registries` / `…-enforce` | `allowed-registries`, `AnyNotIn` over `images.*.registry` | Image registry must be in the platform allowlist |

The `-enforce` name suffix is what lets both packs run at once: ArgoCD never sees two Applications claiming the same `ClusterPolicy`.

### 4.2 `disallow-latest-tag`

`foreach` over `request.object.spec.containers` **and** `request.object.spec.initContainers` denying `element.image == "*:latest"` OR `contains(image, ':') == false`. (`foreach` over an absent list is a no-op, so Pods without initContainers are unaffected.) `background: true`.

Note the interaction with the platform's own pipeline: `app-ci` pushes **both** `:latest` and `:<commit sha>` for every build, precisely so workloads can reference the immutable one. `adhar-console` deliberately tracks `:latest`. Both live in `adhar-system`, which is excluded — see §4.4.

### 4.3 `restrict-image-registries` — the allowlist

Deny when **any** container's registry is not in: `harbor-core.adhar-system.svc.cluster.local`, `harbor.<host>[:port]`, `cgr.dev` (Chainguard — the base-image standard), `registry.k8s.io`, `gcr.io`, `ghcr.io`, `quay.io`, `docker.io`, `index.docker.io`, `public.ecr.aws`.

This is now matched against the **normalised** registry Kyverno parses out of each reference (`images.containers.*.registry` / `images.initContainers.*.registry`) rather than a string prefix on the raw image. The old prefix form denied a bare `nginx:1.27` — whose raw string starts with neither `docker.io/` nor anything else in the list — so the rule would have blocked ordinary Docker Hub images the instant it enforced. That alone made it un-enforceable.

**This is still the loosest of the three.** `docker.io`, `ghcr.io` and `quay.io` are broad enough that the rule verifies very little; tighten to Harbor (including proxy-cache projects) plus `cgr.dev` before enforcing it.

### 4.4 Namespaces that are never blocked

Every rule, at **both** stages, excludes the platform's own control loop **twice over** — by name and by label:

* **by name**: `adhar-system`, `kube-system`, `kube-public`, `kube-node-lease`, `local-path-storage`, `crossplane-system`, `kpack-system`, `cert-manager`, `cosign-system`;
* **by label**: any namespace carrying `adhar.io/plane: control`, which the ApplicationSet stamps on every namespace it creates (`managedNamespaceMetadata`).

The policy that shipped before had `kube-system` excluded from `disallow-latest-tag` but **not** `adhar-system` — and `adhar-system` is where every platform component, the whole Tekton build plane, and the deliberately-`:latest` console run. Enforcing that policy as written would have blocked the platform's own pods. That is fixed: `adhar-system` and `kube-system` are in every rule's exclude block, and the label guard covers namespaces added later.

### 4.4 Exceptions (`manifests/exceptions/*.yaml`)

`kyverno.io/v2beta1` `PolicyException` objects scope the **baseline PSS** policies (not the supply-chain ones) away from platform components that legitimately need host paths / privilege / non-`nonroot` users: `kind.yaml` (kube-system, local-path-storage, coredns, etcd…), `argocd.yaml`, `console.yaml`, `crossplane.yaml`, `ingress-nginx.yaml`. Each lists explicit `policyName`+`ruleNames` (including `autogen-*` variants) and a namespace/name match — the surgical alternative to whole-namespace exclusion.

## 5. Compliance profiles (`security/policy-packs`) 🔜

Two opt-in profiles layered on the baseline, `enabled: "false"` everywhere:

- `cis.yaml` — 9 `cis-*` ClusterPolicies (`cis-disallow-privileged-containers`, `cis-disallow-host-namespaces`, `cis-disallow-host-path`, `cis-require-run-as-nonroot`, `cis-disallow-privilege-escalation`, `cis-require-drop-all-capabilities`, `cis-require-resource-requests-limits`, `cis-disallow-default-namespace`, `cis-require-seccomp-runtimedefault`).
- `soc2.yaml` — 5 `soc2-*` ClusterPolicies (`soc2-require-pinned-images`, `soc2-require-workload-labels`, `soc2-restrict-image-registries`, `soc2-require-pod-probes`, `soc2-audit-networkpolicy-presence`).

Every policy is `validationFailureAction: Audit`, labeled `adhar.io/policy-pack: {cis|soc2}` and `app.kubernetes.io/part-of: policy-packs` (so PolicyReports filter per profile), name-prefixed to coexist with the baseline's cluster-scoped policies, and excludes platform namespaces.

## 6. Runtime layer 🔜

ADR-0019's "runtime closes the loop" — a *legitimately signed* image doing illegitimate things — is covered by catalog packages `security/falco` and `security/tetragon`, both present but `enabled: "false"` in every appset and not yet wired to the signing pipeline. Design-only for now.

## 7. Staged rollout — Audit → Enforce, and how to switch

### 7.1 What is Audit and what can be Enforced

| | Audit (shipped, on) | Enforce (shipped, off) |
|---|---|---|
| `verify-image-signatures` | ✅ enabled in local, production and gitops appsets | ⬜ `supply-chain-policies-enforce`, `enabled: "false"` |
| `disallow-latest-tag` | ✅ | ⬜ same package |
| `restrict-image-registries` | ✅ | ⬜ same package — **tighten the allowlist first** (§4.3) |
| `verify-supply-chain-images` (narrow, `application/supply-chain`) | — | ✅ **already Enforce** for `harbor-core…/library/*` outside `adhar-system` (§2.3) |
| baseline PSS `restricted`, `policy-packs` CIS/SOC2, `namespace-governance` | ✅ Audit | not in scope here |

### 7.2 The switch

Enforcement is **package enablement** — the same one-line Git edit as every other ADR-0004 rollout, with no redeploy and no hand-patched policy:

```yaml
# platform/stack/adhar-appset-{local,production,gitops}.yaml
# and the mirrored platform/stack/environments/<env>/config.yaml
- name: "supply-chain-policies"           # AUDIT  — always on
  enabled: "true"
  manifestPath: "security/supply-chain-policies/manifests/audit"
- name: "supply-chain-policies-enforce"   # ENFORCE — opt-in, default off
  enabled: "false"                        # <-- THE KNOB
  manifestPath: "security/supply-chain-policies/manifests/enforce"
```

Flip `supply-chain-policies-enforce.enabled` to `"true"` in the target environment's appset **and** in `platform/stack/environments/<env>/config.yaml` (`TestEnvironmentConfigsMatchAppSets` fails the build otherwise), commit, let ArgoCD sync.

**Leave the Audit pack enabled.** The two coexist by design, and `TestLocalProductionAppSetParity` requires the locally-enabled set to stay a subset of production — disabling Audit in one environment only would break it.

Finer granularity, both documented in the package README:

* **one policy at a time** — delete the other two files from `manifests/enforce/`; they are separate files for exactly this reason;
* **one namespace at a time** — add a `namespaceSelector` (e.g. `adhar.io/supply-chain: enforce`) to the enforce policies' `match` block and label namespaces in. This is the recommended first move on a live cluster.

Why an appset element rather than a values flag or a seed-time template: stack packages have no Helm values plane, the ApplicationSet renders with `goTemplateOptions: [missingkey=error]` (so an optional per-element key is not available), and `BuildCustomizationSpec` — the only data a `*.yaml.tmpl` receives at seed time — has no enforcement field. Shipping both directories and letting the element choose is the mechanism the repo already has.

### 7.3 Hygiene

Policies are plain ArgoCD-synced manifests (SSA, self-heal). `failurePolicy: Ignore` on every platform webhook keeps the admission path fail-**open**, so a restarting Kyverno on a single node cannot wedge the apiserver (ADR-0012). That is a deliberate trade: **when Kyverno is down, Enforce admits unverified images.** Enforcement is only as available as Kyverno. (`hack/verify-supply-chain.sh` sets `failurePolicy: Fail` on its own scratch policy precisely so the drill cannot report a false pass.)

## 8. Failure modes

- **Kyverno unavailable** → webhooks fail-open (`Ignore`); admission proceeds unverified. Acceptable in Audit; when enforcing, this is the availability/security trade-off the ADR flags.
- **Rekor unreachable** (`verifyImages` keyless) → in Audit with `required: false` the check is advisory and does not fail admission; air-gapped/enforce deployments must run a private Sigstore or fall back to key-based signing (ADR-0019 consequence; `cosign` package's `TrustRoot` CRD is the air-gap hook).
- **Cosign package namespace collision** → resolved by renaming Tekton's Secret (`WEBHOOK_SECRET_NAME=tekton-webhook-certs`) and pinning its `WEBHOOK_PORT`, so cosign lives in `adhar-system` (ADR-0011); kpack keeps `kpack-system` because it hardcodes the same Secret name; documented in the production appset comment and CONFLICTS.md.
- **Trivy scan-job pressure** → `scanJobsConcurrentLimit: 5` + local disablement bound the churn.

## 9. Testing

### 9.1 The enforcement drill — `hack/verify-supply-chain.sh`

The one test that matters before flipping the knob, because it exercises the parts Audit structurally cannot report on: whether Kyverno can **reach** the registry, **trust** its TLS, and **parse a signature made the way this platform makes them**.

It is self-contained and repeatable, and it never touches the platform's own key or policies:

1. refuses to run unless `kubectl --context kind-adhar get nodes` succeeds, the Kyverno CRDs are installed, `kyverno-admission-controller` has a ready replica, and Harbor answers `/v2/`;
2. generates a **throwaway** cosign keypair in a temp dir (never `cosign-key` — the drill must not be able to mint a platform-trusted signature);
3. `cosign copy`s **two different** tiny public images into the platform Harbor as `library/adhar-supplychain-drill:signed` and `:unsigned` (two different upstream digests — signing is by digest, so one manifest under two tags would prove nothing; the script asserts the digests differ);
4. signs only `:signed`, with `--tlog-upload=false`, exactly as the Tekton Task does, and proves locally that `cosign verify` accepts it and rejects the other;
5. applies an **Enforce** `ClusterPolicy` scoped to a scratch namespace and to that one repository, pinned to the throwaway public key, and waits for `.status.ready`. It asks for `failurePolicy: Fail` so a down webhook cannot fake a pass — though the platform's `features.forceFailurePolicyIgnore` overrides that back to `Ignore`, which is why the preflight hard-requires a ready `kyverno-admission-controller` and why the DENY half is the load-bearing assertion (a denial can only come from a webhook that actually ran);
6. asserts **unsigned → DENIED**, **signed → ADMITTED** (server-side dry-run: the full admission chain runs, nothing is persisted, no image pull is needed);
7. deletes the policy, the namespace, the temp keys and — best effort — the Harbor repository, via an `EXIT` trap. `KEEP=1` leaves the scratch objects for inspection.

Exits non-zero on any unexpected outcome. Overrides: `KUBE_CONTEXT`, `HARBOR_HOST`, `HARBOR_USER`/`HARBOR_PASS` (default: read from the `harbor-push` Secret, same credential the pipelines use), `SIGNED_SRC`/`UNSIGNED_SRC`.

What it proves: **signature enforcement works on this cluster, for a signature of the shape the platform produces.** What it does not prove: that the *platform's own* `cosign-key` signatures verify (that needs a real `app-ci` build to have run), nor that the `disallow-latest-tag` / `restrict-image-registries` rules are safe to enforce against the running workload set — read `kubectl get clusterpolicyreport` for that.

### 9.2 Still missing 🔜

- **Kyverno `chainsaw`/`kyverno-test`** unit cases (per exemplar 0023 §10): `disallow-latest-tag` denies `nginx:latest` and admits a pinned digest; `restrict-image-registries` denies `some.random.io/x` and admits `cgr.dev/chainguard/static`. No such policy test exists in-repo today.
- **e2e** (`tests/e2e/bootstrap`): assert the three Audit ClusterPolicies exist and are `.status.ready` after sync.
- **Manual verification**: `kubectl get clusterpolicyreport` surfaces Audit findings; `kubectl get cip` lists Sigstore ClusterImagePolicies (none defined).

## 10. Code & file map

| Path | Responsibility |
|---|---|
| `platform/stack/packages/security/supply-chain-policies/manifests/audit/*` | The 3 supply-chain ClusterPolicies, **Audit** — enabled everywhere |
| `platform/stack/packages/security/supply-chain-policies/manifests/enforce/*` | The same 3 policies, **Enforce** + digest pinning, `-enforce` names — **the knob**, off by default |
| `platform/stack/packages/security/supply-chain-policies/README.md` | What the platform signs with, the staging recipe, the excluded namespaces |
| `platform/stack/packages/security/supply-chain-policies/adhar-package.yaml` | Marketplace contract (depends on `kyverno`; `supply-chain` optional — it provides `cosign-key`) |
| `platform/stack/packages/application/supply-chain/manifests/00-foundation.yaml` | `cosign-keygen` hook Job → durable `cosign-key` Secret; `adhar-pipeline` SA |
| `platform/stack/packages/application/supply-chain/manifests/60-build-pipelines.yaml` | The `cosign-sign` Task — `--key=k8s://adhar-system/cosign-key --tlog-upload=false` |
| `platform/stack/packages/application/supply-chain/manifests/40-verify-images.yaml` | Narrow always-Enforce `verify-supply-chain-images` + `cosign-policy-sync` key-injection Job |
| `hack/verify-supply-chain.sh` | The enforcement drill (§9.1) |
| `platform/stack/packages/security/kyverno-policies/manifests/install.yaml` | Baseline PSS `restricted` policies (chart 3.9.0) |
| `platform/stack/packages/security/kyverno-policies/manifests/exceptions/{kind,argocd,console,crossplane,ingress-nginx}.yaml` | Per-component `PolicyException`s for baseline PSS |
| `platform/stack/packages/security/kyverno-policies/{values.yaml,generate-manifests.sh}` | Audit + fail-open config; chart render script |
| `platform/stack/packages/security/cosign/manifests/install.yaml` | Sigstore policy-controller 0.10.7 (CIP/TrustRoot CRDs + webhook) in `adhar-system` |
| `platform/stack/packages/security/cosign/{values.yaml,generate-manifests.sh}` | policy-controller render |
| `platform/stack/packages/security/trivy/manifests/install.yaml` | trivy-operator 0.33.1 + 12 report CRDs |
| `platform/stack/packages/security/trivy/{values.yaml,generate-manifests.sh}` | scan limits, TTL, namespace pin |
| `platform/stack/packages/security/policy-packs/manifests/{cis,soc2}.yaml` | Opt-in CIS (9) / SOC2 (5) profiles, Audit 🔜 |
| `platform/stack/packages/application/harbor/values.yaml` | Registry choke point; `trivy.enabled` per environment |
| `platform/stack/adhar-appset-{local,production,gitops}.yaml` | The two `supply-chain-policies` / `supply-chain-policies-enforce` elements — where the knob lives |
| `platform/stack/environments/{local,production}/config.yaml` | The mirror of the above; a parity test enforces the match |
| `docs/PRODUCTION.md`, `docs/ROADMAP.md` (Phase 3) | Enforcement checklist + status |

## 11. Milestones

- **M1 — Audit everywhere (done)**: the pack ships, is enabled in every appset, and its rules are now *correct* (§2.1) rather than vacuous — so the reports mean something.
- **M2 — Enforce is a switch, not a rewrite (done)**: `manifests/enforce/` ships alongside `manifests/audit/`, wired disabled; `hack/verify-supply-chain.sh` proves enforcement works on a cluster before anyone flips it.
- **M3 — Flip an environment**: turn `supply-chain-policies-enforce` on (signatures first, narrowed by `namespaceSelector`), after tightening the registry allowlist and adding chainsaw policy tests as a gate.
- **M4 — Golden-path base images + attestations**: `adhar-templates` + Buildpacks run images base on `cgr.dev`; in-cluster pipelines emit SBOM/provenance attestations so the policy can add an `attestations:` block (ADR-0018).
- **M5 — Keyless in-cluster**: a private Sigstore (Fulcio/Rekor) or policy-controller `TrustRoot`, retiring the long-lived `cosign-key`.
- **M6 — Runtime**: enable Falco/Tetragon and wire runtime findings alongside the SBOM inventory.

## 12. Risks

- **Two verifiers, one enabled**: Kyverno `verifyImages` and Sigstore policy-controller both exist; policy-controller defines no live `ClusterImagePolicy`, so Kyverno is the de-facto verifier. Drift risk remains until one is retired.
- **Enforce without signing pipelines**: `app-ci` signs by default, so platform-built images are covered — but anything deployed from an image the pipelines did **not** build is not signed, and `required: true` denies it. Narrow with a `namespaceSelector` first (§7.2) rather than flipping an environment wide.
- **Long-lived signing key**: `cosign-key` is a durable in-cluster secret with a static password (`adhar`) baked into the manifests. Fine for local; on a real cluster it should come from Vault/ESO, and rotating it invalidates every existing signature.
- **Fail-open vs security**: `failurePolicy: Ignore` trades enforcement guarantees for cluster availability on constrained nodes; revisit for HA production (ADR-0012).
- **Sigstore dependency**: keyless verification needs Fulcio/Rekor; air-gap requires a private Sigstore (`TrustRoot`) or key-based fallback.
- **Allowlist breadth**: `restrict-image-registries` currently permits `docker.io/*`/`ghcr.io/*`/`quay.io/*` — appropriate for Audit, must tighten before Enforce or it verifies nothing meaningful.
</content>
</invoke>
