# Adhar Package Marketplace

Adhar ships 80+ platform packages across nine categories. As the catalog opens to
**community-contributed packages** (ROADMAP Phase 3), every package — first-party or
community — must satisfy a machine-checkable **compatibility contract** and declare its
**supply-chain provenance**. This document defines that contract, the provenance
requirements, the stability tiers, and the submission process.

The contract is a single file, `adhar-package.yaml`, placed at the root of each package
directory:

```
platform/stack/packages/<category>/<name>/
├── adhar-package.yaml        # ← the marketplace contract (this document)
├── generate-manifests.sh
├── values.yaml
└── manifests/
```

It is validated against [`marketplace.schema.json`](./marketplace.schema.json) (JSON
Schema draft-07) by [`hack/validate-packages.sh`](../../../hack/validate-packages.sh),
which runs on every pull request (the `package-contracts` job in
[`.github/workflows/pr.yaml`](../../../.github/workflows/pr.yaml)) and as part of
`make lint`. **Every package in the catalog carries one today** — a package
directory without a contract fails CI.

> Related docs: [`CONFLICTS.md`](./CONFLICTS.md) tracks shared-namespace collisions
> (a separate concern from this contract). Provenance follows
> [ADR-0019 — Secure software supply chain](../../../docs/adr/0019-secure-supply-chain-chainguard.md).

---

## 1. The compatibility contract

Every `adhar-package.yaml` declares the following. Required fields are marked ✅.

| Field | Req | Purpose |
|---|:--:|---|
| `apiVersion` | ✅ | Always `marketplace.adhar.io/v1alpha1`. |
| `kind` | ✅ | Always `AdharPackage`. |
| `name` | ✅ | Package name — **must equal the directory name** and the ArgoCD Application name. |
| `category` | ✅ | One of the nine real categories (see below) — **must equal the top-level directory**. |
| `version` | ✅ | Package version (semver), conventionally the upstream chart version. |
| `appVersion` | | Upstream application/image version, if distinct from `version`. |
| `description` | ✅ | One-line summary shown in the marketplace listing. |
| `maintainer` | ✅ | `{ name, email?, url?, firstParty? }` — who keeps it current. |
| `license` | ✅ | SPDX identifier of the upstream project (e.g. `Apache-2.0`). |
| `homepage` | | Upstream project/docs URL. |
| `adharCompatibility` | ✅ | `{ minVersion, maxVersion?, notes? }` — Adhar platform version range this package is verified against. |
| `dependencies` | | Other packages that must be enabled first (`{ name, category?, versionConstraint?, optional? }`). |
| `provenance` | ✅ | Supply-chain block — see §2. |
| `planeAffinity` | ✅ | `control-plane` \| `data-plane` \| `any`. |
| `stability` | ✅ | `alpha` \| `beta` \| `stable` \| `community` — see §3. |
| `resources` | | Footprint hints for capacity planning / local gating. |
| `keywords` | | Search tags. |

### Categories (the enum matches the real directory layout)

`ai` · `application` · `backup` · `core` · `data` · `infrastructure` ·
`observability` · `plugins` · `security`

The validator enforces that `name` and `category` match the package's directory path —
the filesystem is the source of truth, so a contract cannot silently drift from where it
lives.

### `adharCompatibility` — the version range

`minVersion` is the lowest Adhar platform version (`globals/project.go` `Version`) the
package is verified against; `maxVersion` is optional and, when omitted, declares
open-ended forward compatibility. This lets the marketplace hide or warn on packages that
predate (or postdate) the running platform version.

### `dependencies` vs. conflicts

`dependencies` are packages that **must be enabled** for this one to work (e.g. `alloy`
depends on `loki-stack` as its log sink; `kargo` depends on `cert-manager` for webhook
TLS). Mutually-**exclusive** packages are the inverse concern and are tracked in
[`CONFLICTS.md`](./CONFLICTS.md), not here.

### `planeAffinity`

- **control-plane** — management/GitOps/controller logic (e.g. `kargo`).
- **data-plane** — per-node agents or workload-tier components (e.g. `alloy` DaemonSet).
- **any** — operators whose controller is control-plane but whose jobs/agents run
  wherever workloads land (e.g. `trivy`).

---

## 2. Provenance requirements (per ADR-0019)

An IDP is a supply-chain amplifier: whatever the platform bakes in, every team inherits.
[ADR-0019](../../../docs/adr/0019-secure-supply-chain-chainguard.md) defines the
supply-chain contract. The `provenance` block records how each package meets it:

| Provenance field | Req | Meaning |
|---|:--:|---|
| `sourceRepo` | ✅ | Canonical repo the manifests are generated/vendored from. |
| `upstreamChart` | | Upstream Helm chart reference (OCI or repo/chart). |
| `signed` | ✅ | Artifacts carry a cryptographic signature. |
| `cosignKeyless` | ✅ | Signing uses **Cosign keyless** (Sigstore Fulcio + Rekor) — mandated when `signed: true`. |
| `scanned` | ✅ | Reference images are scanned (Trivy) before release. |
| `scanner` | | Scanner used (e.g. `trivy`). |
| `sbom` | | Path/URI of the SBOM (SPDX/CycloneDX). Required once `scanned: true`. |
| `attestation` | | SLSA provenance attestation reference (Rekor index / in-toto URI). |

The three pillars every submission is measured against:

1. **Cosign keyless signature.** Release artifacts are signed with Cosign keyless —
   Fulcio short-lived certs bound to the CI OIDC identity, logged in Rekor. No long-lived
   signing keys. First-party packages are signed by the Adhar GoReleaser pipeline.
2. **Trivy scan.** Reference images are scanned for vulnerabilities before release;
   Harbor re-scans on push and can block critically-vulnerable pulls (ADR-0019 §3).
3. **SBOM.** An SPDX SBOM is emitted and attached so "which running images contain
   package X @ version Y" is a query, not an archaeology project.

> **Mark provenance honestly.** `signed`/`scanned` must reflect reality. A community
> package that is not yet signed sets `signed: false`, `cosignKeyless: false` and carries
> `stability: community` until it is promoted. Overstating provenance is the one thing
> review will reject outright.

First-party packages (`maintainer.firstParty: true`) set `sourceRepo` to
`https://github.com/adhar-io/adhar` and inherit the Adhar release pipeline's provenance.
That pipeline ([`.goreleaser.yaml`](../../../.goreleaser.yaml)) emits, per release:

* an **SPDX SBOM per release archive** (`sboms:`, catalogued by syft);
* **Cosign keyless signatures** (`signs:`) over `checksums.txt` — which covers every
  archive — and over each SBOM, as `<artifact>.sig` + `<artifact>.pem`;
* **Cosign keyless signatures** (`docker_signs:`) over the pushed images and multi-arch
  manifests, recorded in Rekor.

The certificates are Fulcio short-lived certs bound to the release workflow's GitHub
OIDC identity (`id-token: write`), so there is no signing key to leak. Contracts
therefore point `provenance.sbom` at the release assets rather than a per-package file.
Verify a release:

```bash
cosign verify-blob checksums.txt \
  --signature checksums.txt.sig --certificate checksums.txt.pem \
  --certificate-identity-regexp 'https://github.com/adhar-io/adhar/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

cosign verify ghcr.io/adhar-io/adhar:<version> \
  --certificate-identity-regexp 'https://github.com/adhar-io/adhar/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

---

## 3. Stability tiers

| Tier | Meaning | Provenance bar |
|---|---|---|
| `alpha` | Experimental; API/manifests may change without notice. | Best-effort. |
| `beta` | Usable; interfaces stabilizing; not yet load-tested at scale. | Signed + scanned expected. |
| `stable` | Production-ready; first-party maintained; enabled in curated sets. | Full: signed keyless + scanned + SBOM. |
| `community` | Externally contributed, not yet first-party promoted. | Declared honestly; may be unsigned pending promotion. |

A `community` package graduates to `beta`/`stable` when a maintainer adopts it, its
provenance meets the full bar, and it passes review. `stability` is independent of
`adharCompatibility` — a `stable` package can still be pinned to a narrow version range.

---

## 4. Validation

Validate every contract in the tree:

```bash
hack/validate-packages.sh     # or: make validate-packages
```

Scaffold the contract for a package that does not have one yet:

```bash
hack/gen-package-contracts.sh            # or: make gen-package-contracts
hack/gen-package-contracts.sh --dry-run  # list what would be created
```

The generator is **idempotent — it never overwrites an existing contract**, it only
creates missing ones, so it is safe to re-run and safe to hand-edit the result
afterwards. It derives `name`/`category` from the directory, `version`/`appVersion` and
`provenance.upstreamChart` from `generate-manifests.sh`, `dependencies` from the
platform capabilities the manifests reference (CNPG, External Secrets, Keycloak, MinIO,
Kafka, cert-manager), `stability` and `resources.localSafe` from the ApplicationSet
enabled gates, and `planeAffinity` from the workload profile in
`adhar-appset-workload.yaml`. Descriptions, licenses and homepages come from a curated
table inside the script (falling back to the package README / manifest header).
Scaffolded values are a starting point, not a verdict — review them.

The script finds all `adhar-package.yaml` files, validates each against
`marketplace.schema.json`, and additionally checks that `name`/`category` match the
directory layout (the name comparison is case-insensitive — one legacy directory,
`core/Kamaji`, is capitalised while the package name must be a lowercase DNS label).
It exits non-zero on any violation, so it is CI-friendly, and CI runs it on every PR.

Validator resolution:

1. **Preferred** — `python3` with the `jsonschema` module:
   `pip install jsonschema pyyaml`.
2. **Fallback** — the `check-jsonschema` CLI: `pipx install check-jsonschema`
   (directory cross-checks are skipped in this mode).

Example run:

```
OK    application/kargo/adhar-package.yaml
OK    observability/alloy/adhar-package.yaml
OK    security/trivy/adhar-package.yaml
...

All package contracts are valid.
```

---

## 5. Contribution / submission process (community packages)

1. **Scaffold the package.** Create
   `platform/stack/packages/<category>/<name>/` with the usual
   `generate-manifests.sh`, `values.yaml`, and `manifests/` (deploying into
   `adhar-system`). Follow the conventions of existing packages in the category.
2. **Check for conflicts.** Run the collision scan in
   [`CONFLICTS.md`](./CONFLICTS.md) and resolve any shared-namespace clashes.
3. **Write `adhar-package.yaml`.** Scaffold it with
   `hack/gen-package-contracts.sh` and then fill it in. Set `maintainer.firstParty:
   false`, `stability: community`, and record provenance **honestly** — if it is not yet
   signed/scanned, say so. (The generator's defaults describe a *first-party* package;
   a community submission must correct `maintainer`, `stability` and `provenance`.)
4. **Validate locally.** `hack/validate-packages.sh` must pass.
5. **Wire it (disabled).** Add the package to the ApplicationSet with `enabled: "false"`
   so it ships wired-but-off (the enabled-gating model, ADR-0004). Curated core enablement
   is a separate maintainer decision.
6. **Open a PR** with DCO sign-off. Review covers: contract validity, honest provenance,
   conflict-freedom, resource footprint, and category fit.
7. **Promotion.** Once a maintainer adopts the package and its provenance reaches the full
   bar (Cosign keyless + Trivy scan + SBOM), `stability` graduates to `beta`/`stable` and
   `firstParty` may flip to `true`.

---

## Reference example

Every package directory ships a contract, so any of them is a reference. The
hand-written ones are the fullest:

- [`application/kargo/adhar-package.yaml`](./application/kargo/adhar-package.yaml) — control-plane, `stable`.
- [`security/trivy/adhar-package.yaml`](./security/trivy/adhar-package.yaml) — `any` plane, `stable`.
- [`observability/alloy/adhar-package.yaml`](./observability/alloy/adhar-package.yaml) — data-plane, `stable`.
- [`observability/fluent-bit/adhar-package.yaml`](./observability/fluent-bit/adhar-package.yaml) — data-plane, an opt-in alternative to `alloy`.
