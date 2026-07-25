# ADR-0013: SSO bootstrap via idempotent in-cluster config job

**Status**: Accepted · **Date**: 2026-07

## Context

ADR-0008 makes Keycloak the platform IdP, but something has to *create* the realm, groups, users, OIDC clients, and the Kubernetes Secrets that downstream packages consume — on a fresh cluster, with no human in the loop, ordered after Keycloak is actually up. Options:

- **Keycloak Operator / realm CRs** — another operator on the critical path; realm import CRs cover creation but not the "export client secrets back into Kubernetes" half of the loop
- **Terraform/Crossplane provider for Keycloak** — heavy dependency for a bootstrap-time concern; still needs the secret-export half solved separately
- **A scripted Job shipped with the keycloak package** — direct Admin-API calls, full control of ordering and secret export, no extra operators

## Decision

The keycloak package ships a **config Job run as an ArgoCD Sync hook** (sync-wave ordered after the Keycloak Deployment and its CNPG database). The job provisions the `adhar` realm, groups, test users, and one OIDC client per integrated service, then writes the client credentials into the `keycloak-clients` Secret; per-service `ExternalSecret`s (ADR-0009) project those values into each package's expected shape (`grafana-oidc`, `headlamp-oauth2-proxy`, …). The SSO chain is therefore: config job → `keycloak-clients` → ESO → per-service secrets → oauth2-proxy/native OIDC.

Hard-won rules encoded in the job (each one is a fix for an observed production failure, commented inline):

- **Idempotent re-runs, but reconciling**: on every sync the job re-PUTs client configs (redirect URIs, scopes) so config changes reach existing realms; the early-exit for already-provisioned realms **verifies a sentinel client exists** — bare realm existence is not proof of provisioning (a job killed mid-run once left a clientless realm, and the naive early-exit then rebuilt `keycloak-clients` with empty values, silently breaking every SSO login)
- **Token lifetime is managed**: master-realm admin tokens default to 60s; the job's first API call raises `accessTokenLifespan` and re-fetches, because the job's own package installs and downloads take minutes between API calls (expired-token 401s mid-provisioning were the original half-provisioned-realm cause)
- **Secret rebuild path**: if `keycloak-clients` is missing but the realm is healthy (e.g. after a namespace migration), the job rebuilds the Secret from the live realm instead of failing
- **Hook semantics are understood**: hook manifests are *not* part of ArgoCD's desired-state diff, so editing the job script alone never triggers auto-sync — rollout of job changes requires an explicit sync (or any non-hook change in the same package). This is documented here because it cost real debugging time.

## Consequences

- ✅ Fresh `adhar up` reaches "log in to any UI with SSO" with zero manual Keycloak clicks
- ✅ The full credential chain is declarative-adjacent: Git holds the job + ExternalSecret pointers; only Keycloak and the cluster hold values (ADR-0009 preserved)
- ✅ Re-syncing the keycloak package is safe at any time; drift in client config is reconciled, existing sessions unaffected
- ⚠️ The job is imperative bash against the Admin API — the price of covering both provisioning *and* secret export; kept reviewable in one file (`keycloak-config.yaml`)
- ⚠️ Job-script changes need an explicit sync to take effect (hook semantics above)
- ⚠️ Integrating a new service means: client payload file + job wiring + an ExternalSecret — a documented three-step recipe, deliberately boring
