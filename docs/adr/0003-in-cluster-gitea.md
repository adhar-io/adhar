# ADR-0003: Self-hosted in-cluster Gitea as the platform source of truth

**Status**: Accepted · **Date**: 2026-07

## Context

GitOps needs a Git server. Options: depend on an external SaaS (GitHub/GitLab), require a customer-provided Git server, or self-host one inside the platform. An external dependency breaks the "one command, works offline, no accounts required" promise for local and air-gapped use, and would make bootstrap depend on credentials that don't exist yet.

## Decision

Ship **Gitea in-cluster** as part of the bootstrap foundation. The controller:

- Installs Gitea and waits for API readiness (deployment + pod + HTTP probe)
- Creates the `environments` and `packages` repositories via API (`auto_init: true`, default branch `main`)
- Populates them from `platform/stack/` and pushes (`git push -f origin "$branch:main"`)
- Wires ArgoCD to Gitea through repo credential secrets and a dedicated `gitea-argocd` ClusterIP service

Gitea is the platform's **system of record**, not necessarily the developers' primary forge: the `GitRepository` CRD supports GitHub/GitLab/Bitbucket as first-class providers, and organizations can mirror or replace content sources while ArgoCD still syncs from in-cluster Gitea (or directly from an external forge if they choose).

## Consequences

- ✅ Zero external dependencies: works on a laptop, in CI, and air-gapped
- ✅ Platform state (including every customization) is inspectable and editable at `gitea.<domain>` from minute one
- ✅ Uniform bootstrap across all providers — no "bring your own Git" configuration matrix
- ⚠️ Gitea becomes stateful critical infrastructure: production requires external PostgreSQL (CNPG), replicas, and backup of its storage (Production Guide)
- ⚠️ Two-way sync with an external forge is the organization's responsibility (mirror push/pull); the platform only guarantees the in-cluster source of truth
