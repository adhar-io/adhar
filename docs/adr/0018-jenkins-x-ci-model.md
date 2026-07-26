# ADR-0018: CI on the platform — Jenkins X pipeline model on Tekton, promotion via GitOps

**Status**: Accepted (Tekton + Buildpacks + Kargo packages shipped; Jenkins X layer is the adoption target for Roadmap Phase 3 golden paths) · **Date**: 2026-07

## Context

The platform ships delivery *infrastructure* — Tekton (`application/tekton`), Cloud Native Buildpacks (`application/buildpack`), Kargo (`application/kargo`), Argo Workflows/Events — but infrastructure is not a developer experience. Raw Tekton demands that every team author Pipelines, Tasks, triggers, and webhook plumbing before their first build runs; that is exactly the "integration project" an IDP exists to eliminate (pillar 3, ADR-0015). Options for the CI experience layer:

- **Classic Jenkins** — enormous plugin surface, controller-centric state, groovy pipelines; wrong architecture for a GitOps-native, Kubernetes-native platform
- **Forge-native CI (GitHub Actions / GitLab CI / Gitea Actions)** — couples CI to a specific forge, runs outside platform guardrails, splits observability and secrets handling from the platform; kept *supported* (teams may bring it) but not the paved road
- **Raw Tekton + hand-rolled triggers** — maximum flexibility, maximum per-team toil; no shared convention for versioning, tagging, or promotion
- **Jenkins X (jx)** — an opinionated, CNCF-graduated-adjacent layer *on top of Tekton*: pipeline-as-code (`jenkins-x.yml`) with inheritable shared pipeline catalogs, Lighthouse for forge webhooks + ChatOps (`/test`, `/approve`, `/lgtm`), automatic semantic version tagging and release notes, and native GitOps promotion via PRs to environment repos — which is precisely the structure of Adhar's `environments` repo (ADR-0003)

## Decision

Adopt the **Jenkins X pipeline model as the paved-road CI experience**, executing on the Tekton engine the platform already ships:

- **Pipeline-as-code with inheritance**: applications carry a minimal `jenkins-x.yml`; the heavy lifting lives in a platform-owned **pipeline catalog** (build → test → scan → sign → package → promote-PR) that teams inherit and override per step. Fixing a pipeline defect in the catalog fixes every consumer on their next run — the golden-path property.
- **Lighthouse owns forge events**: webhook handling, PR ChatOps, merge automation (Keeper), and status reporting work identically against Gitea, GitHub, and GitLab — matching the platform's multi-forge posture (ADR-0003). CI triggers are forge-portable by construction.
- **Builds produce platform-conformant artifacts**: images are built with **Cloud Native Buildpacks** on Chainguard/Wolfi run images where golden paths apply (no Dockerfile required, SBOM generated at build), pushed to Harbor, signed and attested per the supply-chain contract (ADR-0019). The pipeline catalog is where that contract is *enforced*, not suggested.
- **CI ends at a Git commit — promotion is not CI's job**: the pipeline's terminal act is opening a version-bump PR against the environments repo. From there **Kargo orchestrates promotion** dev → staging → prod (Roadmap Phase 2) and ArgoCD deploys (ADR-0004). CD stays declarative; Jenkins X's own CD-era machinery is explicitly *not* adopted — ArgoCD is the platform's sync engine, full stop.
- **Previews integrate, not duplicate**: Jenkins X's built-in preview mechanism is superseded by the platform's ApplicationSet-based previews (ADR-0017); the pipeline merely posts the preview URL to the PR.
- **Escape hatch is explicit**: teams with existing forge-native CI keep it, but must still land in the environments repo through the same promotion PR contract and meet the same artifact requirements (ADR-0019) — the paved road is default, not mandatory.

## Consequences

- ✅ A scaffolded service (Roadmap Phase 3 golden paths) gets working CI — webhook, pipeline, versioning, registry push, promotion PR — with zero per-team pipeline authoring
- ✅ One pipeline catalog is the platform's supply-chain enforcement point: signing, SBOM, and scanning cannot be "forgotten" by a team (ADR-0019)
- ✅ ChatOps and merge automation come from Lighthouse rather than per-forge bespoke bots; forge choice stays reversible (pillar 8)
- ⚠️ Jenkins X carries its own opinions (version streams, repo layout conventions) — the platform adopts its *pipeline + Lighthouse* layer selectively, and that boundary ("jx for CI, ArgoCD/Kargo for CD") must be defended in reviews or the two GitOps engines will fight
- ⚠️ Another control-plane component set (Lighthouse, pipeline operator) joins the platform namespace with all ADR-0011/0012 obligations (collision scan, probe tuning, resource budget); on the local profile CI runs are batched and previews capped
- ⚠️ The Tekton package is currently mutually exclusive with `open-function` (vendored Tekton collision, ADR-0011) — a standing catalog constraint recorded in CONFLICTS.md
