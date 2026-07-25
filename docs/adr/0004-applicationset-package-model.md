# ADR-0004: Single ApplicationSet with enabled-gated package list

**Status**: Accepted · **Date**: 2026-07

## Context

The platform ships ~87 package directories and wires 69 of them for deployment. Options for driving ArgoCD:

- **One hand-written Application per package** — 69 files of boilerplate; no single view of what's deployed
- **Git directory generator** — "everything in the repo deploys"; no per-environment gating, accidental deploys on merge
- **App-of-apps** — hierarchical but still per-package Application manifests
- **One ApplicationSet with an explicit list generator** — each package is one list element with metadata

Local clusters add a hard constraint: a single Kind node cannot run all 69 packages (OOM), so partial enablement must be first-class, not an afterthought.

## Decision

Drive all platform packages from a **single ApplicationSet** per environment (e.g. `adhar-appset-local.yaml`). Every package is declared as a list element:

```yaml
- name: "cert-manager"
  enabled: "true"          # ← the gate
  namespace: "adhar-system"  # per-element; the shared platform namespace (ADR-0011)
  category: "security"
  manifestPath: "security/cert-manager/manifests"
```

The `namespace` field is templated into each Application's destination, so nearly all packages share `adhar-system` (ADR-0011) while incompatible ones (e.g. `open-function`) opt out per element. Runtime toggling of individual packages patches the affected elements rather than re-applying the whole file — the operational rules are in ADR-0014.

A generator `selector` on `enabled: "true"` means **everything is wired, only enabled entries deploy**. Environment configs (`environments/<env>/config.yaml`) carry the same package schema, giving each environment its own enablement set. Packages themselves are **pre-rendered manifests** (`helm template` via each package's `generate-manifests.sh` + `values.yaml`), so ArgoCD syncs plain YAML — no in-cluster Helm evaluation, and diffs in Git are the exact cluster diff.

## Consequences

- ✅ Enabling/disabling any platform capability is a one-line Git change
- ✅ One file answers "what is deployed in this environment?"
- ✅ Pre-rendered manifests: reviewable diffs, no chart-repo network dependency at sync time, reproducible installs
- ⚠️ The list is explicit — adding a package means adding a directory *and* one list entry (accepted: explicitness is the feature)
- ⚠️ Version bumps require re-running `generate-manifests.sh` (automatable in CI)
- ⚠️ Per-environment values require either per-env manifest paths (e.g. `manifests/dev`) or overlays — convention documented in the Customization Guide
