# Getting Started with Adhar

By the end of this page you will have a complete Internal Developer Platform running on your laptop — GitOps, identity, CI, a container registry, observability and a developer portal — and you will have deployed a service onto it. It takes about 10 minutes, most of it waiting.

This is the fast path. [User Guide](USER_GUIDE.md) is the task reference for everything you do afterwards; [Customization Guide](CUSTOMIZATION.md) is how you change the defaults.

## Contents

1. [Before you start](#1-before-you-start)
2. [Install the CLI](#2-install-the-cli)
3. [Start the platform](#3-start-the-platform)
4. [Open the consoles](#4-open-the-consoles)
5. [Deploy your first service](#5-deploy-your-first-service)
6. [Turn on another capability](#6-turn-on-another-capability)
7. [Run it on a cloud](#7-run-it-on-a-cloud)
8. [Tear it down](#8-tear-it-down)
9. [If something goes wrong](#9-if-something-goes-wrong)
10. [Where to go next](#10-where-to-go-next)

## 1. Before you start

| Requirement | Version | Why |
| --- | --- | --- |
| **Docker** | v20.10+ | Runs the Kind node. Give it at least 8 GB of RAM |
| **kubectl** | v1.24+ | Not required to bootstrap — Adhar drives the cluster — but you will want it |
| **RAM** | 8 GB minimum, 16 GB comfortable | The curated local profile runs 32 applications |
| **Disk** | 20 GB+ free | Container images and volumes |
| **CPU** | 4+ cores | |

Adhar provisions Kubernetes **v1.37.0** by default. Override it with `adhar up --kube-version v1.36.0` — the flag applies to every provider, not just Kind.

## 2. Install the CLI

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/adhar-io/adhar/main/scripts/install.sh | bash

# or Homebrew
brew install adhar-io/tap/adhar
# equivalently: brew tap adhar-io/tap && brew install adhar
# only if you have HOMEBREW_REQUIRE_TAP_TRUST set, trust the tap once first:
#   brew trust --tap adhar-io/tap

# or download an archive from https://github.com/adhar-io/adhar/releases

adhar version
```

## 3. Start the platform

```bash
adhar up
```

Progress streams to your terminal. When the success banner appears the platform is converged.

What just happened, in order:

1. A Kind node was created with the default CNI and kube-proxy disabled, host ports `8443`/`8080` mapped to the Gateway.
2. The foundation was installed from manifests **embedded in the binary** (no network fetch): Gateway API CRDs → Cilium → the Cilium Gateway → ArgoCD → Gitea → Crossplane.
3. Three Git repositories were created in the in-cluster Gitea under the `adhar` org and seeded from the platform stack: **`packages`** (every package's rendered manifests), **`environments`** (per-environment package sets) and **`templates`** (service scaffolding templates).
4. An ArgoCD ApplicationSet wiring **94 entries across 91 packages** was applied. Locally a curated **32** are enabled — a single node cannot run the full catalogue. ArgoCD synced them from Gitea.

Variants worth knowing:

| Command | Effect |
| --- | --- |
| `adhar up --port 9443` | Different HTTPS host port (HTTP auto-derives as port − 363, here `9080`) |
| `adhar up --recreate` | **Destructive.** Delete the existing Kind node and rebuild |
| `adhar up --dry-run` | Preview without creating anything |
| `adhar up --kube-version v1.36.0` | Pin the Kubernetes version |
| `adhar up --dev-password` | Set the ArgoCD and Gitea admin passwords to `developer` |
| `adhar up` (again) | Safe. Resumes an interrupted bootstrap from the gate that was pending |

## 4. Open the consoles

Every service is a subdomain of `adhar.localtest.me`, which resolves to `127.0.0.1` — no `/etc/hosts` editing. The TLS certificate is self-signed locally, so accept the browser warning.

| Service | URL | What it is |
| --- | --- | --- |
| Adhar Console | `https://console.adhar.localtest.me:8443` | The developer portal — catalog, golden paths, cloud shell |
| ArgoCD | `https://argocd.adhar.localtest.me:8443` | Every deployed package, as an Application |
| Gitea | `https://gitea.adhar.localtest.me:8443` | The platform's source of truth |
| Keycloak | `https://keycloak.adhar.localtest.me:8443` | Identity for every other service |
| Grafana | `https://grafana.adhar.localtest.me:8443` | Metrics, logs (Loki), traces (Tempo) |
| Headlamp | `https://headlamp.adhar.localtest.me:8443` | Kubernetes UI |
| Hubble | `https://hubble.adhar.localtest.me:8443` | Live network flows |
| Harbor | `https://harbor.adhar.localtest.me:8443` | Container registry |
| Tekton | `https://tekton.adhar.localtest.me:8443` | CI pipeline runs |
| LibreDB Studio | `https://libredb.adhar.localtest.me:8443` | Browser SQL IDE over the platform's databases |
| MinIO | `https://minio.adhar.localtest.me:8443` | Object storage |
| Policy Reporter | `https://policy-reporter.adhar.localtest.me:8443` | Kyverno policy results |

The full list for your platform is whatever ships an `HTTPRoute`: `kubectl get httproute -A`.

Credentials:

```bash
adhar get secrets                 # every service credential the CLI knows about
adhar get secrets -p argocd       # just ArgoCD (user: admin)
adhar get secrets -p keycloak     # admin + first user
```

`-p` accepts `argocd`, `gitea`, `keycloak`, `adhar-console`, `vault`, `postgres`, `redis`, `harbor`, `rustfs`. Gitea's day-0 default is `gitea_admin` / `r8sA8CPHD9!bt6d` — rotate it before anyone else can reach the platform.

Check health at any time:

```bash
adhar get status      # AdharPlatform conditions + per-package health
adhar get apps        # ArgoCD application sync/health states
```

## 5. Deploy your first service

The quickest real deployment uses a template from the Gitea `templates` repo:

```bash
adhar apps deploy hello --template microservice --namespace hello
adhar apps status hello
```

That fetched `microservice.yaml` from `adhar/templates`, substituted the name and namespace, and created a `CompositeApplication` — the platform's own application API. Crossplane expanded it into an ArgoCD Application, which deployed the manifests. Run `adhar apps deploy` with a bad template name to list what is available.

Already have a repo? Point ArgoCD straight at it:

```bash
adhar apps deploy my-app \
  --repo https://github.com/org/repo \
  --path manifests/ \
  --dest-namespace my-team \
  --wait
```

Or build from source through the platform's signed supply chain (buildpacks → Trivy → Cosign → Harbor → deploy):

```bash
adhar push my-app --git-url https://github.com/org/repo --wait
```

The richer paths — the four Console golden paths (`microservice`, `frontend`, `data-pipeline`, `ml`) and per-pull-request preview environments — are in [User Guide §4](USER_GUIDE.md#4-deploy-an-application) and [§5](USER_GUIDE.md#5-preview-a-pull-request).

## 6. Turn on another capability

62 of the 94 wired entries are off locally. Turning one on is a platform change, and platform changes are edits to the Adhar stack followed by `adhar upgrade`:

```bash
# In your adhar checkout — flip enabled: "false" -> "true" for the package,
# in BOTH files (a parity test enforces that they match):
#   platform/stack/adhar-appset-local.yaml
#   platform/stack/environments/local/config.yaml

adhar upgrade --diff-only     # see exactly what would change
adhar upgrade                 # converge, re-apply the ApplicationSet, re-push the stack
```

ArgoCD picks the new Application up and deploys it; when it reports `Healthy` it is live at `https://<name>.adhar.localtest.me:8443` if it ships an HTTPRoute.

> A single Kind node cannot run all 94. Check `kubectl top nodes` before enabling anything heavy, and read the mutual-exclusion notes in `platform/stack/packages/CONFLICTS.md` — a few packages (`vault`/`openbao`, `vllm-cpu`/`vllm-gpu`) must not both be on.

Full details, including changing a package's values and adding your own: [Customization Guide](CUSTOMIZATION.md).

## 7. Run it on a cloud

The same binary and the same package stack target AWS EKS, Azure AKS, GCP GKE, DigitalOcean DOKS, Civo, or any conformant cluster. Declare a config file and pass it with `-f`:

```bash
adhar up -f adhar-config.yaml --env production
adhar get status
```

Cloud runs use the production profile (**76 of the 94 entries enabled**), install the controller manager in-cluster for continuous reconciliation, render the foundation in HA mode, and put the Gateway behind a real LoadBalancer with cert-manager-issued TLS.

Credentials, regions, node pools and per-cloud caveats: [Provider Guide](PROVIDER_GUIDE.md). Before real workloads: [Production Guide](PRODUCTION.md).

## 8. Tear it down

```bash
adhar down              # remove the local Kind node and Adhar state
                        # (a cloud environment needs its config:
                        #  adhar down -f config.yaml --env dev)
adhar up --recreate     # or: destroy and rebuild in one step
```

The platform is fully reconstructable from the binary plus your config, so rebuilding is a routine move, not a last resort.

## 9. If something goes wrong

| Symptom | First thing to try |
| --- | --- |
| `adhar up` fails immediately | `docker ps` — is Docker running? Port taken? `adhar up --port 9443` |
| Bootstrap stopped part-way (Ctrl-C, sleep, timeout) | Just run `adhar up` again — it resumes from the pending gate |
| A URL does not load | `adhar get apps` — the app must be `Healthy` and ship an HTTPRoute |
| Browser TLS warning | Expected locally (self-signed). Production uses cert-manager |
| Pods `Pending`, node under pressure | Disable packages you enabled, or give Docker more RAM |
| Everything wedged | `adhar up --recreate` |

Deeper failure signatures — Gateway never `Programmed`, empty ArgoCD, wave-stuck syncs — are in [Troubleshooting](TROUBLESHOOTING.md). Community: [Issues](https://github.com/adhar-io/adhar/issues) · [Slack](https://join.slack.com/t/adharworkspace/shared_invite/zt-26586j9sx-QGrIejNigvzGJrnyH~IXww)

## 10. Where to go next

| You want to… | Read |
| --- | --- |
| Use the running platform day to day | [User Guide](USER_GUIDE.md) |
| Change what the platform ships | [Customization Guide](CUSTOMIZATION.md) |
| Understand how it works | [Architecture](ARCHITECTURE.md) |
| Go to a cloud | [Provider Guide](PROVIDER_GUIDE.md) |
| Run it for real | [Production Guide](PRODUCTION.md) |
