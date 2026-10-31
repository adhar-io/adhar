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
| Team | Console: Keycloak group `ws-team-<slug>` (now `ws-team-<org>.<slug>`, see the 2026-10-31 amendment); templates: a Backstage `Group` descriptor | nothing adhar-controlplane; the project composition *assumes* the group name |
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
| `adhar.io/team` | team and below | owning team slug (Keycloak group `ws-team-<organisation>.<team>`) |
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

**Team** — people who own projects. Composes nothing that costs money: a Gitea team inside the organisation (so repositories can be granted to the team) and the status record of its Keycloak group `ws-team-<organisation>.<team>`. Its reason to exist as an object is that projects reference it and selectors can find everything the team owns.

**Project** — a product. Composes:
- the project's **home namespace** `<project>`, which is also its **Kargo Project** (Kargo requires a namespace named after the project, labelled `kargo.akuity.io/project`): Kargo `Project`, `ProjectConfig` (dev and test auto-promote, prod never — one object to read and one to audit, exactly what `app-env-register` used to apply by hand), and the git/image credentials Kargo discovers by label;
- one **Environment per entry of `environments`** (default `dev`, `test`, `prod`); each entry may pin a `namespace` (the seeded platform project uses `dev`/`test`/`prod` outright, matching what `adhar up` creates) and a `tier`;
- the Argo CD **AppProject** `<project>`, whose destinations are the project home and every environment namespace, and whose source repos are the organisation's Gitea repositories plus anything in `allowedSourceRepos`;
- optionally the project's Gitea repository;
- the project's **shared promotion chain**: one Kargo `Stage` per environment, named after it (`dev` → `test` → `prod`, in the project's order), whose `requestedFreight` lists one origin per application of the project (its Warehouse `<app>`) — see the 2026-10 amendment below;
- `status.environments[]` with the resolved `{name, namespace, tier}` — the one place the application composition and the paved-road Tasks look up where an environment lives.

**Environment** — a namespace with guardrails and *access*. Unchanged in shape, but it now knows its organisation / team / project and its `access` tier: `admin` for the team in dev and test, `view` in prod (configurable), because production changes arrive through a Release, not `kubectl`. `tier` is `dev | test | prod`.

**Application** — a deployable inside a project. Composes, per environment of the project it belongs to: an Argo CD `Application` `<project>-<app>-<env>` in AppProject `<project>` deploying `deploy/envs/<env>` into that environment's namespace; a Kargo `Warehouse` `<app>` for the application's image repository, which the project's shared Stages promote dev → test → prod, each promotion `git-clone → kustomize-set-image (by digest) → git-commit → git-push → argocd-update` (2026-10 amendment: there is no per-application Stage and no per-application environment list). It reads the project's `status.environments` through an observe-only `Object`, so an application never guesses a namespace. This is the content of the old imperative `app-env-register` Task made declarative; the Task now applies a `Application` and nothing else.

**Release** — the act of putting a freight into an environment. Composes a Kargo `Promotion` (`stage: <env>` — the project's shared Stage — `freight: <id>` from the application's Warehouse); Kargo's webhook fills the steps from the Stage's template. Its status mirrors the promotion's phase. A release is therefore a reviewable, listable, GitOps-committable object — `adhar get releases`, the Console's Releases view and an Argo CD diff all see the same thing — and prod promotion is a `Release` in a PR, not a button.

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

## Amendment (2026-10): environments are the project's, shared by every application

Applications no longer choose environments and no longer own Kargo Stages.

- **One promotion chain per project.** The Project composition composes Kargo Stages named after its environments (`dev`, `test`, `prod`) in the Kargo project namespace `<project>`, labelled `adhar.io/project`, `adhar.io/environment` and `adhar.io/stage-scope=project` (and *not* `adhar.io/application`). The first takes freight directly from the Warehouses, each next one from the previous Stage. Each Stage requests one origin per Application of the project (`{kind: Warehouse, name: <app>}`); Kargo promotes — and auto-promotes, per the ProjectConfig — each origin independently, so applications still move on their own through the same environments.
- **How the Project knows its applications.** It requests every `Application` as a function-go-templating extra resource and keeps those in its namespace that name it and have published `status.delivery` (image, repository, overlay path, branch, soak, verification), which the Application composition writes once it has observed the project. No object has two composite owners: Warehouses and Argo CD Applications are the Application's, Stages and their AnalysisTemplates are the Project's.
- **Promotion steps** are generated per application and gated on the freight's origin (`if: ${{ ctx.targetFreight.origin.name == '<app>' }}`); the image digest is read with `imageFrom(<image>, warehouse('<app>'))`. They update the Argo CD Application `<project>-<app>-<env>`, which authorises `kargo.akuity.io/authorized-stage: <project>:<env>`.
- **Verification** is per environment: Kargo gives an AnalysisRun only `ctx.project`/`ctx.stage`, never the promoted origin, so one AnalysisTemplate `<env>-health` carries a set of metrics per application, each passing for an application not deployed there yet. A broken application therefore holds its environment back — intended, since the environment is shared.
- **`Application.spec.parameters.environments`** is deprecated and ignored (kept in the schema so existing objects stay valid). A subset filter was rejected: an application that skipped `test` could never reach `prod` through a shared chain. The `ProjectEnvironments` condition reports `OverrideIgnored` when the field disagrees with the project.
- **Migration** is a re-render: per-application Stages `<app>-<env>` and AnalysisTemplates `<app>-<env>-health` stop being composed and are garbage-collected; the shared Stages appear. Freight is kept (it belongs to the Warehouse); verification history is not carried over and does not need to be — the new `dev` re-promotes the newest freight (a no-op commit), verifies it, and `test` follows. Prod moves only on a new Release against Stage `prod`.

## Amendment (2026-10-31): identity is Keycloak's alone, and team groups name their organisation

The Console keeps no members, roles or invitations of its own any more. It adds and removes people in Keycloak groups, and an organisation's people are exactly the members of those groups.

- **Three groups, created by the Console and bound by the compositions.** `org-<org>` holds every member and is `view` on the organisation's home namespace. `org-<org>-admin` holds its administrators, who are members too, and is `admin` on every namespace of the organisation. `ws-team-<org>.<team>` is each team: `admin` in its project's namespace and its dev and test environments, `view` in prod. The platform's own admin groups (`platform-admin`, `platform-engineer`, `super-admin`) administer every organisation in the Console; `platform-admin` and `platform-engineer` are cluster-admin on Kubernetes already.
- **The team group names its organisation.** It was `ws-team-<team>`, and a team slug is unique only inside one organisation, so a team called `platform` in any tenant was the same Keycloak group as the platform's own team — which `Project/adhar` binds as admin of `adhar`, `dev` and `test`. A `.` joins the two slugs because neither may contain one, so no other pair spells the same group; a `-` would not do, since `(acme, platform-x)` and `(acme-platform, x)` collide. For the same reason the Organisation XRD refuses a name ending in `-admin`: its member group would be another organisation's admin group.
- **Workspace administration is not RBAC.** The realm-wide `ws-role-owner` / `ws-role-admin` / `ws-role-billing` ClusterRoleBindings in `core/adhar-console/manifests/console-rbac.yaml` made an owner of one organisation an administrator of all of them, because a ClusterRoleBinding cannot say "admin of *this* organisation". The Console answers organisation-scoped checks itself from the verified token's `org-<org>-admin`, and those bindings are removed.
- **Deploy order.** The Console release ships first and, on start, copies existing members and team memberships into the new groups, removing nothing; in the platform organisation `adhar`, whose admin group is admin of `prod` too, former workspace admins are reported to the platform admins instead of copied into `org-adhar-admin`. The platform release follows: its `adhar upgrade` re-applies the compositions, which moves the bindings to the qualified team groups, and its stack push prunes the `ws-role-*` bindings. In the other order team members lose their namespaces until the Console has copied them. The legacy `ws-role-*` and `ws-team-<team>` Keycloak groups stay in place, granting nothing, for an operator to delete.
- Guards: `platform/controllers/adharplatform/tenancy_groups_test.go`.
