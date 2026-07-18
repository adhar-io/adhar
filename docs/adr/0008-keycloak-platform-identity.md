# ADR-0008: Keycloak as the platform identity provider (OIDC everywhere)

**Status**: Accepted (wiring lands in Roadmap Phase 1) · **Date**: 2026-07

## Context

The platform ships many UIs (ArgoCD, Gitea, Grafana, Console, Harbor, …), each with its own local account system. Without a decision, users accumulate per-service credentials, offboarding is unreliable, and RBAC is inconsistent — disqualifying for production. Options: per-service accounts (status quo), delegating entirely to an external IdP (breaks local/air-gapped parity), or shipping an identity provider in the platform.

## Decision

**Keycloak is the platform's identity provider.** Every identity-aware component authenticates via **OIDC against Keycloak**; authorization flows from **group claims** mapped to each service's role model (ArgoCD RBAC, Gitea orgs, Grafana roles, Kubernetes RBAC via OIDC).

- Keycloak ships as a platform package (enabled in the curated core), backed by CNPG PostgreSQL in production
- Enterprise directories (LDAP/AD/SAML/social) federate *into* Keycloak — services only ever see Keycloak, so upstream IdP changes touch one place
- Bootstrap credentials (`gitea_admin`, ArgoCD `admin`) are day-0 only: the target flow rotates them into Vault as break-glass credentials once SSO is wired
- Kubernetes API access uses OIDC group claims for human users; workloads use service accounts / workload identity, never Keycloak

## Consequences

- ✅ One login, one offboarding switch, one audit point for humans; same experience local and production
- ✅ RBAC becomes group management in Keycloak instead of N per-service admin panels
- ⚠️ Keycloak becomes availability-critical for *login* (running sessions and reconciliation are unaffected) — HA in production, and break-glass local accounts documented per service
- ⚠️ Each package's OIDC wiring is per-service configuration that must be maintained as packages upgrade (tracked in the [Roadmap](../ROADMAP.md))
