# ADR-0009: Secrets — External Secrets Operator as sync plane, Vault as source of truth, never Git

**Status**: Accepted · **Date**: 2026-07

## Context

A GitOps platform has a structural tension: Git is the only write path (ADR-0001), but secrets must never be committed. Options considered: encrypted-in-Git (SealedSecrets/SOPS — secrets still live in Git, rotation is a commit, re-encryption on key rotation is painful), per-service secret handling (no policy, no audit), or an external source of truth synced into the cluster.

## Decision

**Secrets live outside Git in a dedicated secret store; External Secrets Operator (ESO) syncs them into Kubernetes.**

- **ESO is the platform contract**: manifests in Git reference `ExternalSecret` objects — Git carries *pointers*, never values
- **Vault is the default source of truth** (shipped as a package, curated-core enabled); organizations already invested in AWS Secrets Manager / Azure Key Vault / GCP Secret Manager plug those in as ESO `SecretStore`s instead — the manifests don't change
- Production Vault: HA with auto-unseal via cloud KMS; rotation happens in the store, ESO propagates on its refresh interval (scheduled rotation via the platform's CronOperations)
- Enforcement: no-secrets-in-Git is policed (Gitea push scanning / CI secret scanning); etcd encryption at rest is required in production so synced Secrets are protected downstream

## Consequences

- ✅ GitOps purity preserved: full platform state in Git, zero secret material in Git
- ✅ Store-agnostic: swapping Vault for a cloud secret manager is a `SecretStore` change, not an application change
- ✅ Rotation and audit centralize in one system with an API
- ⚠️ ESO + store become part of the platform's critical path for *new* pods that mount secrets (running pods are unaffected) — both need HA in production
- ⚠️ Kubernetes Secrets remain the last hop: RBAC on Secret objects and etcd encryption are mandatory hardening, not optional (see [Production Guide §3](../PRODUCTION.md#3-security-hardening-checklist))
