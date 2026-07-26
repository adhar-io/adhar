# ADR-0019: Secure software supply chain — Chainguard base images, Sigstore signing, policy-enforced admission

**Status**: Accepted (Harbor/Trivy/Cosign/Kyverno packages shipped; enforcement rollout is phased per environment) · **Date**: 2026-07

## Context

An IDP is a supply-chain amplifier: whatever base images, build process, and admission posture the platform bakes into its golden paths gets inherited by every team. The threat model is well documented (SLSA, SolarWinds/xz-style compromises): unpatched CVEs in bloated base images, unsigned artifacts of unknown origin, build tampering between CI and cluster, and no inventory (SBOM) when the next Log4Shell lands. Point tools exist in the catalog — Harbor (registry), Trivy (scanning), Cosign (signing), Kyverno (admission policy) — but tools without a *contract* devolve into optional checkboxes. Options for the base-image layer specifically:

- **Debian/Ubuntu-based images** — familiar, but hundreds of packages nobody uses; every scan reports dozens-to-hundreds of CVEs, and triaging "is this reachable?" burns platform-team time forever
- **Google distroless** — minimal and proven (the Adhar binary itself ships on `distroless/static:nonroot`), but a limited image set and no toolchain for organizations to build *their own* minimal images
- **Chainguard Images (Wolfi-based)** — minimal, daily-rebuilt images targeting zero known CVEs, signed with Sigstore and shipping SBOMs by default; Wolfi is a purpose-built distroless-style distro with a real package ecosystem, and the free tier covers `latest` develop-tier usage while regulated organizations can pay for version pinning — with Wolfi/apko remaining open should Chainguard's terms change (pillar 8 exit path)

## Decision

The platform defines a **supply-chain contract** enforced at build time (ADR-0018's pipeline catalog) and at admission time (Kyverno), with Chainguard as the base-image standard:

1. **Base images: Chainguard/Wolfi by default.** Golden-path templates and Buildpacks run images use `cgr.dev` Chainguard Images (static, glibc-dynamic, language runtimes). The platform's own binary stays on distroless-static; both satisfy the same rule: *no shells, no package managers, no unused packages in production images*. Teams needing custom bases build them from Wolfi with apko/melange rather than reaching for `ubuntu:latest`.
2. **Every artifact is signed and attested.** CI signs images with **Cosign keyless** (Sigstore: Fulcio certificates bound to the pipeline's OIDC identity, Rekor transparency log) — no long-lived signing keys to leak or rotate. Builds attach an **SBOM (SPDX) attestation** (Buildpacks emit these natively; syft covers Dockerfile builds) and provenance attestation targeting SLSA level 2+ (hosted build, signed provenance).
3. **Harbor is the distribution choke point.** Images deploy from the platform Harbor (proxy-cache for upstreams, local project for builds), which scans on push with Trivy and can block pulls of critically-vulnerable images. Air-gapped and rate-limit resilience come free (ADR-0006's offline principle extended to workloads).
4. **Admission verifies, humans don't.** Kyverno `verifyImages` policies require valid signatures + SBOM/provenance attestations for workload namespaces; `security/kyverno-policies` carries the policy pack. Rollout is staged `Audit` → `Enforce` per environment (prod first for enforcement, local stays audit-mode by default so experimentation isn't blocked) — mirroring the enabled-gating model (ADR-0004).
5. **The SBOM inventory is queryable.** SBOM attestations land in Harbor alongside images, so "which running images contain package X @ version Y" is a query, not an archaeology project — the Log4Shell-response requirement.
6. **Runtime closes the loop.** Falco/Tetragon (catalog packages) detect what static controls can't: a *legitimately signed* image doing illegitimate things. Supply-chain integrity is layered, not a single gate.

## Consequences

- ✅ Golden-path services are born compliant: minimal base, signed, SBOM'd, scanned — teams inherit the posture instead of assembling it
- ✅ CVE noise collapses (near-zero-CVE bases make every scanner finding *meaningful*), and vulnerability response becomes an SBOM query plus a rebuild on a daily-refreshed base
- ✅ Keyless signing removes the worst key-management failure mode; verification policy is Git-managed like everything else
- ⚠️ Minimal images have a learning curve — no shell to `kubectl exec` into (debugging uses ephemeral debug containers), no `apt` for quick hacks; this is a feature but requires documentation and habit change
- ⚠️ Chainguard's free tier tracks `latest` only; organizations needing pinned versions choose between paying, mirroring, or building from Wolfi — the platform documents all three rather than pretending the trade-off away
- ⚠️ Keyless verification depends on Sigstore infrastructure (Fulcio/Rekor) — air-gapped deployments must run a private Sigstore stack or fall back to key-based signing; enforcement in `Enforce` mode adds an admission-path dependency that must obey the webhook hygiene rules of ADR-0012/0014
