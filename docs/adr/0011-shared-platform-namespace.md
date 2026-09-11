# ADR-0011: Single shared namespace (`adhar-system`) for platform packages

**Status**: Accepted · **Date**: 2026-07

## Context

Platform packages originally deployed into per-package namespaces (`cert-manager`, `harbor`, `monitoring`, …). That mirrors upstream defaults, but in practice it fragmented the operator experience: credentials, NetworkPolicies, RBAC grants, and troubleshooting (`kubectl get pods -A` archaeology) were spread across ~30 namespaces, and every cross-package reference (Grafana → Loki, oauth2-proxy → Keycloak, ArgoCD → Gitea) needed a fully-qualified cross-namespace service name that broke whenever one side moved. Alternatives:

- **Per-package namespaces (status quo)** — strongest isolation, worst operability; cross-package wiring is N² fully-qualified references
- **Per-category namespaces** (`security`, `observability`, …) — arbitrary boundaries; the wiring problem remains at category edges
- **One shared platform namespace** — one place to look, short service names, single RBAC/quota/backup boundary

## Decision

All platform packages deploy into **`adhar-system`**, the same namespace as the bootstrap foundation — with one exception (buildpack, below). The ApplicationSet destination is still templated per element (`namespace: "{{ .namespace }}"`), but every element now sets `adhar-system`; the field is kept only as a mechanical escape hatch, not as a sanctioned opt-out. Packages that previously opted out (`cosign` → `cosign-system`, `buildpack` → `kpack-system`, `kubeflow` → `kubeflow`, `open-function` → `openfunction`) were re-rendered into `adhar-system`, with the upstream `kind: Namespace` objects dropped and the collisions handled by renaming (see below).

The only namespaces the platform creates outside `adhar-system` are:

- **Kargo `Project` namespaces** (`adhar-environments`) — Kargo requires one namespace per Project; the namespace *is* the Project, so this is a product constraint rather than a packaging choice.
- **Data-plane vclusters** (`dp-<name>`) — separate clusters running tenant workloads (ADR-0023), not platform packages.
- **buildpack (kpack) in `kpack-system`** — kpack's webhook and sigstore's policy-controller (cosign) both hardcode `Secret/webhook-certs` in their binaries and both are enabled; two knative webhooks cannot share that Secret, so the smaller one keeps its own namespace (CONFLICTS.md).

New application environments get their own namespaces; that is the boundary that matters. Platform components do not.

Sharing one namespace removes the boundary upstream charts assume, which creates two failure classes that are now **platform invariants** (documented with a collision-detection script in `platform/stack/packages/CONFLICTS.md`):

1. **Object-name collisions** — two packages defining the same `kind/name` fight over ownership through ArgoCD sync. Some cannot be fixed by renaming (Knative and Tekton both read `config-logging`/`config-observability` by fixed name); such package pairs are mutually exclusive or must be split into separate namespaces.
2. **Service-link env collisions** — Kubernetes injects `<SVC>_PORT` env vars for every Service in the namespace; a generically-named Service (`webhook`, `operator`, `storage`) can hijack another component's flag parsing (observed: cosign's `Service/webhook` set `WEBHOOK_PORT=tcp://…` which crossplane parsed as its `--webhook-port` flag and crashed). Platform components that read config from env set `enableServiceLinks: false`.

A third rule follows from collapsing the last exceptions: **a name a package hardcodes in its binary cannot be renamed in the manifest.** cosign and kpack both hardcode `Secret/webhook-certs` (knative webhook framework), so they are mutually exclusive in one namespace — which is why buildpack keeps `kpack-system`.

Rules for package authors:
- **Never bundle a capability the platform already provides** (Kubeflow Pipelines shipped its own MinIO *and* its own Argo Workflows; both are stripped at generation time).
- **Never ship a `kind: Namespace` object.** An app tracking `Namespace/adhar-system` deletes the platform namespace when pruned, and a vendored Namespace carrying `pod-security.kubernetes.io/enforce: restricted` blocks pod creation platform-wide.
- **Namespace references hide in env values and CLI flags**, not just `namespace:` fields (a stale `OPERATOR_NAMESPACE: trivy-system` env value survived the migration sweep and crashlooped trivy-operator). Grep for `value: <name>-system` and `--*namespace=` when importing charts.
- Run the CONFLICTS.md collision scan before wiring a new package.

## Consequences

- ✅ One namespace answers "what is the platform running": one RBAC boundary, one quota, one backup selector, short stable service names (`gitea-http:3000`, `keycloak:8080`)
- ✅ Cross-package integration (SSO wiring, datasources, repo URLs) uses local names that survive refactors
- ✅ One rule with a one-line exception list: every platform package is in `adhar-system` except buildpack (`kpack-system`); non-platform namespaces are only Kargo Projects (`adhar-environments`) and data-plane vclusters (`dp-*`)
- ⚠️ Isolation between platform packages is reduced to label/NetworkPolicy granularity — acceptable for *platform* components under one operator; tenant workloads still get their own namespaces (ADR-0005)
- ⚠️ Name-collision hygiene is a standing review obligation (CONFLICTS.md scan); upstream charts must be audited, not trusted
- ⚠️ Packages whose binaries hardcode an object name have no escape hatch inside one namespace: cosign and buildpack both hardcode `Secret/webhook-certs`, hence the buildpack exception (CONFLICTS.md)
- ⚠️ PodSecurity enforcement must be set on `adhar-system` deliberately (baseline, not restricted) because eBPF/system packages (Cilium, Falco, Tetragon) need privileged pods
