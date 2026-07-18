# Changelog

All notable changes to the Adhar Platform will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

> **Note**: Project versioning was reset to `0.1.0` in July 2026 to mark the first
> public release from the rebuilt codebase. Release notes for tagged versions are
> generated automatically by GoReleaser from conventional commit messages and
> published on the [GitHub Releases](https://github.com/adhar-io/adhar/releases) page.

## [Unreleased]

### Planned

- Enhanced multi-tenancy support
- Advanced networking policies
- Extended control plane capabilities
- Platform marketplace

---

## [0.1.0] - 2026-07-18

### Added

- **`adhar up` / `adhar down`** — single-command platform lifecycle on a local Kind cluster
- **Two-phase deployment model** — imperative bootstrap (Gateway API CRDs → Cilium → Cilium Gateway → ArgoCD → Gitea), then GitOps-driven reconciliation via ArgoCD ApplicationSet
- **Three Kubernetes controllers** — AdharPlatform, GitRepository, and CustomPackage (`platform.adhar.io/v1alpha1`)
- **Multi-cloud provider abstraction** — AWS EKS, Azure AKS, GCP GKE, DigitalOcean DOKS, Civo K3S, and local Kind
- **Crossplane v2 control plane** — 23 XRDs, 34 Compositions, 5 Functions, packaged as a Configuration `.xpkg`
- **69 platform packages** wired through the ArgoCD ApplicationSet with a curated local-safe core enabled by default
- **Cilium CNI + Gateway API** — eBPF data path, kube-proxy replacement, `adhar-gateway` for HTTP/HTTPS routing
- **Automated release pipeline** — GoReleaser v2 + GitHub Actions: cross-platform binaries, archives, checksums, container images (GHCR), Homebrew tap, and auto-generated release notes on every tag
- **Platform status conditions** — standard Kubernetes conditions on `AdharPlatform` (per-component + aggregate `Ready` carrying the last reconcile error), surfaced by `adhar get status`
- **Package health dashboard** — `adhar get status` shows every platform package's ArgoCD health/sync with summary counts
- **`adhar://` source replication** — restored the CustomPackage feature that pushes local/in-repo application sources into Gitea as `GitRepository` resources
- **Bootstrap E2E test** — `make e2e` verifies the full sequence on Kind: `adhar up` → Ready condition → foundation components → seeded Gitea repos → ArgoCD API → ApplicationSet package health → CLI status → `adhar down`

### Fixed

- Source-directory deletions now propagate to the target Git repository (mirror semantics in the GitRepository reconciler)
- Checking out a tag or branch ref during clone actually resolves the ref (previously silently stayed on HEAD)
- `PatchPasswordSecret` sets the GVK explicitly so secret patching works against real clusters
- Registered `policy/v1` and `autoscaling/v2` in the platform scheme; unknown CRD kinds in embedded manifests decode as unstructured instead of failing
- Embedded install-resource rendering used wrong filesystem paths and misused the override file parameter
- Full unit test suite green (was ~15 known-failing tests)

---

[Unreleased]: https://github.com/adhar-io/adhar/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/adhar-io/adhar/releases/tag/v0.1.0
