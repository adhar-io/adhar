# supply-chain-policies — ADR-0019 admission pack (Audit → Enforce)

Three Kyverno `ClusterPolicy` objects, shipped **twice**: once in Audit mode and
once in Enforce mode. The pair differ only in `failureAction` (and, for the
signature policy, in digest pinning), so **what the Audit stage reports is
exactly what the Enforce stage would block**.

| Policy (audit / enforce) | What it checks |
|---|---|
| `verify-image-signatures` / `verify-image-signatures-enforce` | Platform-built images (`harbor*/library/*`) and Adhar release images (`ghcr.io/adhar-io/*`) carry a valid cosign signature |
| `disallow-latest-tag` / `disallow-latest-tag-enforce` | Workload images carry an explicit, immutable tag |
| `restrict-image-registries` / `restrict-image-registries-enforce` | Workload images come from Harbor or a trusted registry |

## What the platform actually signs with

Read this before touching the attestor: the platform does **not** sign keylessly
in-cluster (a Kind/kubeadm cluster has no ambient OIDC identity Fulcio would
accept). The Tekton `cosign-sign` Task
(`platform/stack/packages/application/supply-chain/manifests/60-build-pipelines.yaml`)
runs:

```
cosign sign --key=k8s://adhar-system/cosign-key --tlog-upload=false \
            --allow-http-registry=true <image>:<tag>
```

* **Key, not keyless.** The keypair is durable and platform-owned: the
  `cosign-keygen` Sync-hook Job in
  `application/supply-chain/manifests/00-foundation.yaml` runs
  `cosign generate-key-pair` **once** and stores `cosign.key` / `cosign.pub` /
  `cosign.password` in Secret `cosign-key` (namespace `adhar-system`). The Job is
  a no-op when the Secret already exists, so the key survives re-syncs and
  cluster restarts. It is *not* ephemeral.
* **No transparency-log entry.** `--tlog-upload=false` means there is nothing in
  Rekor to look up, so the attestor MUST set `rekor.ignoreTlog: true`. The policy
  that shipped before this package pointed at `https://rekor.sigstore.dev`, which
  made every platform-built image fail verification — invisible only because the
  policy could only Audit.
* **Kyverno can read the key directly.** Kyverno runs in `adhar-system` and its
  `kyverno:admission-controller` Role grants `get` on Secrets there, so the
  `keys.secret` attestor reads the live public half. Nothing to copy, nothing to
  keep in sync when the key is rotated.
* **Keyless is kept as an alternative** (`attestors[0].count: 1` — any one entry
  satisfies the rule) for `ghcr.io/adhar-io/*`: `.goreleaser.yaml` `docker_signs`
  runs `cosign sign --yes` inside the `release` GitHub Actions workflow with
  `id-token: write`, so those images carry a Fulcio certificate with issuer
  `https://token.actions.githubusercontent.com` and subject
  `https://github.com/adhar-io/adhar/.github/workflows/release.yaml@refs/tags/<tag>`.

## The enforcement knob

Enforcement is a **package-enablement** switch, exactly like every other
ADR-0004 staged rollout — one line of Git, no redeploy. Two ApplicationSet
elements wire the two directories:

```yaml
# platform/stack/adhar-appset-{local,production}.yaml  (and the mirrored
# platform/stack/environments/<env>/config.yaml)
- name: "supply-chain-policies"           # AUDIT  — always on
  enabled: "true"
  manifestPath: "security/supply-chain-policies/manifests/audit"
- name: "supply-chain-policies-enforce"   # ENFORCE — opt-in, default off
  enabled: "false"
  manifestPath: "security/supply-chain-policies/manifests/enforce"
```

**To enforce in an environment:** set `supply-chain-policies-enforce.enabled` to
`"true"` in that environment's appset **and** in
`platform/stack/environments/<env>/config.yaml` (a parity test keeps the two in
step), commit, and let ArgoCD sync.

Leave the Audit pack enabled. The two packs coexist by design — policy names
differ (`-enforce` suffix) so they never fight over the same object, and the
local↔production parity test requires the locally-enabled set to stay a subset
of production, which disabling Audit in one environment would violate.

**To enforce only one of the three policies,** delete (or `enabled: "false"`-gate)
the other two files in `manifests/enforce/`. They are separate files precisely so
each can be staged independently — signatures first is the usual order, registry
allowlist last (it is the loosest rule and needs tightening first, see below).

**To enforce for only some namespaces,** add a `namespaceSelector` to the
enforce policies' `match` block, e.g.

```yaml
      match:
        any:
          - resources:
              kinds: ["Pod"]
              namespaceSelector:
                matchLabels:
                  adhar.io/supply-chain: enforce
```

and label the namespaces you want gated. That is the recommended first step on a
live cluster: enforce one namespace, watch it, widen.

## Namespaces that are never blocked

Both stages exclude the platform's own control loop twice over — by name and by
label — so an Enforce flip can never brick the platform:

* by name: `adhar-system`, `kube-system`, `kube-public`, `kube-node-lease`,
  `local-path-storage`, `crossplane-system`, `kpack-system`, `cert-manager`,
  `cosign-system`;
* by label: any namespace carrying `adhar.io/plane: control` — which the
  ApplicationSet stamps on every namespace it creates via
  `managedNamespaceMetadata`.

`adhar-system` and `kube-system` appear in **every** rule's exclude block. Gating
the platform's own reconcile loop is an ADR-0012-class cluster-wide risk, and
foundation images are pinned at the release boundary instead.

Note that `application/supply-chain/manifests/40-verify-images.yaml` already
ships a *narrow* always-Enforce policy (`verify-supply-chain-images`) covering
only `harbor-core.adhar-system.svc.cluster.local/library/*` outside
`adhar-system`, with its public key patched in at sync time by the
`cosign-policy-sync` Job. This package is the broader, staged rollout around it;
the two are consistent (same key, same `ignoreTlog` posture).

## Before you enforce

1. `kubectl get clusterpolicyreport -o wide` — the Audit stage must be clean for
   the namespaces you are about to gate.
2. Tighten `restrict-image-registries` — `docker.io`, `ghcr.io` and `quay.io` are
   broad enough that the rule verifies very little. Target end state: Harbor
   (including proxy-cache projects) plus `cgr.dev`.
3. Run `hack/verify-supply-chain.sh` on the cluster. It proves, in a scratch
   namespace with its own throwaway key, that an unsigned image is DENIED and a
   signed one is ADMITTED — i.e. that Kyverno can actually reach Harbor and parse
   the signature on *this* cluster, which is the part that silently fails.
4. Remember `failurePolicy: Ignore` (ADR-0012 webhook hygiene): if Kyverno is
   down, admission proceeds **unverified**. Enforce buys you a gate that is only
   as available as Kyverno. Fail-open is a platform-wide decision, not a
   per-policy one — the kyverno values set
   `features.forceFailurePolicyIgnore.enabled: true`, which overrides any policy
   asking for `failurePolicy: Fail` (the drill included). That is why the drill
   hard-requires a ready `kyverno-admission-controller` before it asserts
   anything.
