# ADR-0026: The concept hierarchy as one declarative control plane

**Status**: Accepted · **Date**: 2026-10

## Context

Adhar's product model has always been stated as a hierarchy:

```
Organisation → Team → Project → Application → Environment → Release
```

but the platform implemented it as six half-connected mechanisms, each owning a
different slice of the truth:

| Concept | Where it lived | What was missing |
|---|---|---|
| Organisation | Console `tenant-provisioner.ts`: Keycloak group `org-<slug>`, a namespace, a Grafana folder, a Gitea org, a Harbor project — all created imperatively by the Console, none visible as a Kubernetes object | nothing declarative; `adhar get` and Argo CD cannot see an organisation exists |
| Team | Console: Keycloak group `ws-team-<slug>`; templates: a Backstage `Group` descriptor | nothing in-cluster; the project composition *assumes* the group name |
| Project | `Project`: one namespace, quota, team RBAC, NetworkPolicy, AppProject, Gitea repo | one namespace is not a project — a project's applications run in **environments**, and the AppProject only allowed deployments into the project namespace, so no application could actually deploy under it |
| Application | `Application` (per-env Argo CD Applications into `<app>-<env>`) **and** the Tekton Task `app-env-register`, which created the same Applications plus the Kargo Project/Warehouse/Stages with `kubectl` | two writers for one object graph; the declarative half knew nothing about Kargo, the imperative half nothing about projects |
| Environment | `Environment`: a namespace with a `tier` — and `platform/stack/environments/<env>` — and the three namespaces `adhar up` creates | three unrelated things called "environment"; `tier` still enumerated `staging`, which [ADR-0021](0021-day2-operations-first-class.md)'s one-environment-model had removed everywhere else |
| Release | — | no object at all; a promotion was a `kargo` CLI invocation or a Console button, with nothing to list, diff or audit |

The labels disagreed too: the project composition stamped `platform.adhar.io/organisation|team|project`, the application composition `adhar.io/application|environment`, the Tekton Task `adhar.io/app`, the agent composition `adhar.io/owner`. No selector could answer "everything belonging to team X".

This is the gap the Console's catalog, the CLI's `adhar get`, cost attribution, policy reporting and the agentic layer ([ADR-0024](0024-agentic-ai-platform.md)) all keep running into: the hierarchy exists in prose, not as objects.

## Decision

**Every level of the hierarchy is a namespaced Crossplane v2 composite resource in `platform.adhar.io`, every object a composition emits carries the same six ownership labels, and nothing else is a source of truth for structure.** Identity (Keycloak groups and their membership) stays with the Console, because people are not infrastructure; everything a group is *granted* is composed from the XR that names the group.

### 1. One label vocabulary

| Label | Set by | Meaning |
|---|---|---|
| `adhar.io/organisation` | every level | tenant slug |
| `adhar.io/team` | team and below | owning team slug (Keycloak group `ws-team-<team>`) |
| `adhar.io/project` | project and below | the product / bounded context |
| `adhar.io/application` | application and below | a deployable |
| `adhar.io/environment` | environment and below | `dev` / `test` / `prod` (the only tiers that exist) |
| `adhar.io/release` | release | the freight (image digest set) a promotion carries |

`adhar.io/plane: workload` marks every namespace the hierarchy creates, so the platform's own namespace and tenant namespaces never blur ([ADR-0011](0011-shared-platform-namespace.md), [ADR-0023](0023-control-dataplane-separation.md)). The `platform.adhar.io/*` ownership labels are retired; `platform.adhar.io/self-service: "true"` stays as the marker for "a composition made this".

### 2. The objects

```
Organisation  adhar            (namespace adhar-system)
└─ Team       platform         organisation: adhar
   └─ Project adhar            team: platform, environments: [dev, test, prod]
      ├─ Environment adhar-dev   ─┐  composed BY the project, one per entry
      ├─ Environment adhar-test  ─┤  (an XR composing XRs: Crossplane v2
      ├─ Environment adhar-prod  ─┘   allows a namespaced XR to emit another)
      └─ Application <app>      project: adhar
         └─ Release <app>-prod-<n>   application: <app>, environment: prod
```

**Organisation** — the tenant. Composes the organisation's home namespace (`<org>`), a ResourceQuota and LimitRange for it, RoleBindings for `oidc:org-<org>-admin` (admin) and `oidc:org-<org>` (view), the Gitea organisation, and publishes the group names it expects on its status so the Console and the project composition read them from one place instead of each spelling the convention.

**Team** — people who own projects. Composes nothing that costs money: a Gitea team inside the organisation (so repositories can be granted to the team) and the status record of its Keycloak group `ws-team-<team>`. Its reason to exist as an object is that projects reference it and selectors can find everything the team owns.

**Project** — a product. Composes:
- the project's **home namespace** `<project>`, which is also its **Kargo Project** (Kargo requires a namespace named after the project, labelled `kargo.akuity.io/project`): Kargo `Project`, `ProjectConfig` (dev and test auto-promote, prod never — one object to read and one to audit, exactly what `app-env-register` used to apply by hand), and the git/image credentials Kargo discovers by label;
- one **Environment per entry of `environments`** (default `dev`, `test`, `prod`); each entry may pin a `namespace` (the seeded platform project uses `dev`/`test`/`prod` outright, matching what `adhar up` creates) and a `tier`;
- the Argo CD **AppProject** `<project>`, whose destinations are the project home and every environment namespace, and whose source repos are the organisation's Gitea repositories plus anything in `allowedSourceRepos`;
- optionally the project's Gitea repository;
- `status.environments[]` with the resolved `{name, namespace, tier}` — the one place the application composition and the paved-road Tasks look up where an environment lives.

**Environment** — a namespace with guardrails and *access*. Unchanged in shape, but it now knows its organisation / team / project and its `access` tier: `admin` for the team in dev and test, `view` in prod (configurable), because production changes arrive through a Release, not `kubectl`. `tier` is `dev | test | prod`.

**Application** — a deployable inside a project. Composes, per environment of the project it belongs to: an Argo CD `Application` `<project>-<app>-<env>` in AppProject `<project>` deploying `deploy/envs/<env>` into that environment's namespace; a Kargo `Warehouse` for the application's image repository and a Kargo `Stage` `<app>-<env>` chained dev → test → prod, each promotion `git-clone → kustomize-set-image (by digest) → git-commit → git-push → argocd-update`. It reads the project's `status.environments` through an observe-only `Object`, so an application never guesses a namespace. This is the content of the old imperative `app-env-register` Task made declarative; the Task now applies a `Application` and nothing else.

**Release** — the act of putting a freight into an environment. Composes a Kargo `Promotion` (`stage: <app>-<env>`, `freight: <id>`); Kargo's webhook fills the steps from the Stage's template. Its status mirrors the promotion's phase. A release is therefore a reviewable, listable, GitOps-committable object — `adhar get releases`, the Console's Releases view and an Argo CD diff all see the same thing — and prod promotion is a `Release` in a PR, not a button.

**AgentWorkload** ([ADR-0024](0024-agentic-ai-platform.md)) keeps its own namespace per agent and environment but now names its `project` and `team`, so an agent is attributable like any other workload.

### 3. Who writes what

- **Console** creates `Organisation` / `Team` XRs when a tenant or team is created, and keeps owning Keycloak group creation and membership. It stops creating namespaces, RoleBindings and Gitea organisations itself.
- **Templates** (`adhar-templates`: organisation, team, project, environment, application, agent-service) render exactly these XRs.
- **Paved road**: `app-env-scaffold` still writes the kustomize overlays into the application repository (reading the environment namespaces from the project's status); `app-env-register` applies one `Application`.
- **`adhar up`** seeds the platform's own tenancy: organisation `adhar`, team `platform`, project `adhar` with environments `dev`/`test`/`prod` on the namespaces of the same name. The platform's own packages still promote through the `adhar-environments` Kargo project (`application/kargo`); applications promote through their project's.

### Alternatives considered

- **Keep the Console as the source of truth and expose it over its API.** Rejected: structure that only exists in one UI's database cannot be reconciled, diffed, or recovered from Git, and every other consumer (CLI, Argo CD, Kyverno, cost reports) would have to call the Console.
- **One flat `CompositeTenant` with nested lists.** Fewer kinds, but a change to one application would re-render the whole tenant, and RBAC on "who may create an application" could not be expressed per level. Rejected.
- **Backstage catalog entities as the model.** The catalog *describes*; it does not provision, and it is read from a Git repository that no controller reconciles against the cluster. The catalog remains a projection of these XRs.
- **Namespace per application per environment (`<app>-<env>`) as before.** Rejected: an environment is where a project's applications meet (shared quota, shared network boundary, one Argo CD destination); per-application namespaces made "the test environment" a set with no object.

## Consequences

- `tier`/environment enumerations everywhere are `dev | test | prod`; `staging` is gone from the XRDs and templates as it already was from the Kargo pipeline and the environments repository.
- The `platform.adhar.io/organisation|team|project` and `adhar.io/app` labels are replaced by the `adhar.io/*` vocabulary above. The Console's namespace views select on `adhar.io/plane=workload`, unchanged.
- Existing `Project` objects re-render: their namespace gains the new labels and the project gains three environment namespaces and a Kargo project. Existing applications registered through the old Task keep working (their `<app>-<env>` namespaces and Kargo project are untouched) until re-onboarded.
- Control-plane RBAC grows the Kargo kinds and `environments` (an XR composing XRs needs permission to create the inner XR).
- `platform/controllers/adharplatform/hierarchy_test.go` pins the vocabulary, the enumerations, the promotion policy (dev/test auto, prod manual — the same rule `application/kargo` applies to the platform itself) and the XRD↔composition parameter parity for every level.
