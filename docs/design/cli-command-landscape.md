# Adhar CLI command landscape (CloudFoundry-inspired)

Status: design + phased implementation. This document defines the developer- and
operator-facing command surface for Adhar, modelled on CloudFoundry's famed
simplicity (`cf push`), mapped onto Adhar's Crossplane control plane so **every
command — from the CLI or the Console — goes through the same control-plane
layer**. It also defines the Org → Project → Team → Application hierarchy.

## Principles

1. **One obvious command per intent.** `adhar push` to ship code; `adhar apps
   deploy` to deploy a template; `adhar logs`/`metrics`/`traces` to observe.
2. **Same path for CLI and Console.** Commands create Crossplane composite
   resources (`CompositeApplication`, `CompositePipeline`, `CompositeDatabase`,
   `CompositeProject`) or trigger the shared supply-chain pipeline — never a
   bespoke CLI-only code path.
3. **Templates + source of truth live in Gitea.** Templates are served from the
   Gitea `templates` repo; scaffolded services get their own Gitea repo.
4. **Guardrails, not gates.** Signed images (cosign) + kyverno admission are
   automatic; developers never wire them by hand.

## Hierarchy: Organization → Project → Team → Application

| Level | Backing primitive | Notes |
|---|---|---|
| Organization | Keycloak realm / top-level group | tenancy boundary; RBAC root |
| Project | `CompositeProject` XR + namespace(s) | quota, network policy, GitOps scope |
| Team | Keycloak group → kube RBAC (`k8s-rbac.yaml`) | platform-admin / engineer / developer / viewer |
| Application | `CompositeApplication` XR (+ `CompositePipeline` for build) | deployed via ArgoCD; observ. auto-covered |

`adhar target --org <o> --project <p>` sets the active scope (like `cf target`),
persisted in CLI config so subsequent commands are scoped automatically.

## Command landscape

### Ship & deploy (developer core)
| Command | Intent | Control-plane path | Status |
|---|---|---|---|
| `adhar push <name> --git-url … [--subpath] [--wait]` | Build source → signed image → deploy, one command | supply-chain `new-service` pipeline (buildpacks→Harbor→kyverno-gated deploy) | ✅ implemented |
| `adhar apps deploy <name> --template <t>` | Deploy from a Gitea template (no build) | Gitea `templates` repo → `CompositeApplication` | ✅ implemented |
| `adhar apps deploy <name> --repo <url> [--path]` | Deploy manifests/Helm from a repo | `CompositeApplication` | ✅ implemented |
| `adhar service new --name <n> --template <t>` | **Scaffold** a new Gitea repo from a skeleton → build → deploy | `scaffold-repo` Task → `new-service` pipeline → `CompositeApplication` | ⏳ next (needs skeletons + scaffold Task) |

### Lifecycle & scale
| Command | Intent | Status |
|---|---|---|
| `adhar apps list` / `status <name>` | Inventory + health (via `CompositeApplication` status) | ✅ |
| `adhar scale <name> --replicas N` | Scale | ✅ |
| `adhar apps restart <name>` | Roll a deployment | ⏳ |
| `adhar apps delete <name>` | Remove (prunes the XR) | ✅ |
| `adhar logs <name>` / `metrics <name>` / `traces <name>` | Observe (Loki / Prometheus / Tempo) | ✅ (surface per-app) |

### Backing services (bind data/cache/bucket)
| Command | Intent | Control-plane path | Status |
|---|---|---|---|
| `adhar service create --engine postgresql\|valkey\|redis` | Provision a DB/cache in the project namespace | `CompositeDatabase` | ✅ (composition) |
| `adhar service create --type object` | Object bucket | `CompositeStorage` | ✅ |
| `adhar bind <app> <service>` | Inject the connection secret into an app | (secret ref wiring) | ⏳ |

### Hierarchy & tenancy (operator core)
| Command | Intent | Status |
|---|---|---|
| `adhar org create/list` | Manage organizations | ⏳ |
| `adhar project create/list` | Manage projects (`CompositeProject`) | ✅ (`adhar project`) |
| `adhar team add/list --role platform-engineer\|developer\|viewer` | Team membership → Keycloak group → kube RBAC | ⏳ |
| `adhar target --org --project` | Set active scope (`cf target`) | ⏳ |
| `adhar login` | OIDC login to the platform (Keycloak) | ⏳ |

## Phased plan
1. **Phase 1 (done):** `adhar push`, `apps deploy --template` (Gitea-served), unified `CompositeApplication` path for CLI + Console.
2. **Phase 2:** `scaffold-repo` Tekton Task + language skeletons → `service new --template` fully scaffolds a new Gitea repo, then builds + deploys (the "create new app from Gitea templates, fully automated" flow).
3. **Phase 3:** `adhar login` + `adhar target` + `org`/`team` commands → the full Org→Project→Team scoping, with every command scoped to the active target.
4. **Phase 4:** `adhar bind`, `restart`, and Console parity on all of the above.

## Non-goals / notes
- The Console lives in a separate repo; it must target the same XRDs + the Gitea
  `templates` repo + the `new-service` pipeline. Keep this document as the shared
  contract both surfaces implement.
- `cf`-style app manifests (`adhar.yaml`) that declare push + services + routes
  in one file are a Phase 3/4 convenience layered over these commands.
