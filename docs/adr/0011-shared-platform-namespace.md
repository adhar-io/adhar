# ADR-0011: Single shared namespace (`adhar-system`) for platform packages

**Status**: Accepted · **Date**: 2026-07

## Context

Platform packages originally deployed into per-package namespaces (`cert-manager`, `harbor`, `monitoring`, …). That mirrors upstream defaults, but in practice it fragmented the operator experience: credentials, NetworkPolicies, RBAC grants, and troubleshooting (`kubectl get pods -A` archaeology) were spread across ~30 namespaces, and every cross-package reference (Grafana → Loki, oauth2-proxy → Keycloak, ArgoCD → Gitea) needed a fully-qualified cross-namespace service name that broke whenever one side moved. Alternatives:

- **Per-package namespaces (status quo)** — strongest isolation, worst operability; cross-package wiring is N² fully-qualified references
- **Per-category namespaces** (`security`, `observability`, …) — arbitrary boundaries; the wiring problem remains at category edges
- **One shared platform namespace** — one place to look, short service names, single RBAC/quota/backup boundary

## Decision

All platform packages deploy into **`adhar-system`**, the same namespace as the bootstrap foundation. The ApplicationSet destination is templated per element (`namespace: "{{ .namespace }}"`), so a package that *cannot* share the namespace can opt out — the only current exception is `open-function`, which vendors Knative/Tekton objects whose hardcoded names collide with the core `tekton` package.

Sharing one namespace removes the boundary upstream charts assume, which creates two failure classes that are now **platform invariants** (documented with a collision-detection script in `platform/stack/packages/CONFLICTS.md`):

1. **Object-name collisions** — two packages defining the same `kind/name` fight over ownership through ArgoCD sync. Some cannot be fixed by renaming (Knative and Tekton both read `config-logging`/`config-observability` by fixed name); such package pairs are mutually exclusive or must be split into separate namespaces.
2. **Service-link env collisions** — Kubernetes injects `<SVC>_PORT` env vars for every Service in the namespace; a generically-named Service (`webhook`, `operator`, `storage`) can hijack another component's flag parsing (observed: cosign's `Service/webhook` set `WEBHOOK_PORT=tcp://…` which crossplane parsed as its `--webhook-port` flag and crashed). Platform components that read config from env set `enableServiceLinks: false`.

Rules for package authors:
- **Never ship a `kind: Namespace` object.** An app tracking `Namespace/adhar-system` deletes the platform namespace when pruned, and a vendored Namespace carrying `pod-security.kubernetes.io/enforce: restricted` blocks pod creation platform-wide.
- **Namespace references hide in env values and CLI flags**, not just `namespace:` fields (a stale `OPERATOR_NAMESPACE: trivy-system` env value survived the migration sweep and crashlooped trivy-operator). Grep for `value: <name>-system` and `--*namespace=` when importing charts.
- Run the CONFLICTS.md collision scan before wiring a new package.

## Consequences

- ✅ One namespace answers "what is the platform running": one RBAC boundary, one quota, one backup selector, short stable service names (`gitea-http:3000`, `keycloak:8080`)
- ✅ Cross-package integration (SSO wiring, datasources, repo URLs) uses local names that survive refactors
- ✅ Per-element destination keeps an escape hatch for genuinely incompatible packages
- ⚠️ Isolation between platform packages is reduced to label/NetworkPolicy granularity — acceptable for *platform* components under one operator; tenant workloads still get their own namespaces (ADR-0005)
- ⚠️ Name-collision hygiene is a standing review obligation (CONFLICTS.md scan); upstream charts must be audited, not trusted
- ⚠️ PodSecurity enforcement must be set on `adhar-system` deliberately (baseline, not restricted) because eBPF/system packages (Cilium, Falco, Tetragon) need privileged pods
