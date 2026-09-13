# Package conflicts in the shared `adhar-system` namespace

Every platform package deploys into `adhar-system` — there are no per-package
namespace opt-outs left (ADR-0011). The only namespaces outside `adhar-system`
that the platform creates are the two sanctioned ones:

- **Kargo `Project` namespaces** (`adhar-environments`) — Kargo *requires* one
  namespace per Project; the namespace *is* the Project.
- **Data-plane vclusters** (`dp-<name>`) — separate clusters, not platform
  packages (ADR-0023).

Sharing one namespace removes the boundary upstream charts assume, which
produces two classes of conflict. Both are checked by the collision scan at the
bottom of this file — run it before enabling a new package.

## 1. Object name collisions

Two packages that define the same `kind/name` will fight: each one's ArgoCD
Application claims ownership, so they flap between OutOfSync and Synced and
overwrite each other's content on every sync.

| Object | Packages | Status |
|---|---|---|
| `Secret/webhook-certs` | cosign, buildpack (kpack) | RESOLVED by namespace — both binaries hardcode the name (`webhook.Options{SecretName: "webhook-certs"}` in sigstore's and kpack's `cmd/webhook/main.go`), so buildpack is the one package kept in its own `kpack-system` namespace (ADR-0011 exception). Never move it into adhar-system while cosign is enabled. |
| `Secret/webhook-certs` | cosign, tekton | RESOLVED — tekton reads the name from `WEBHOOK_SECRET_NAME`, so its generator renames it to `tekton-webhook-certs`. |
| `ConfigMap/config-logging`, `config-observability` | knative, open-function, **tekton** | knative/open-function are disabled. kpack's copies are renamed `buildpack-config-*` (and its `CONFIG_*_NAME` env vars repointed) by its generator. |
| `ConfigMap/config-defaults`, `config-tracing`, `config-registry-cert`, `feature-flags`, `pipelines-info` | open-function, **tekton** | open-function vendors Knative's and Tekton's copies. Disabled. |
| `Service/webhook` → `WEBHOOK_PORT` env | **cosign** (policy-controller) vs. any pod that parses env with a strict decoder | RESOLVED per-consumer — Kubernetes injects `WEBHOOK_PORT=tcp://<ip>:443` for every pod in the namespace, and chaos-mesh's dashboard reads `WEBHOOK_PORT` as an integer, so it crash-looped with `converting 'tcp://10.107.33.255:443' to type int`. cosign hardcodes the Service name in its binary, so the consumer opts out: `enableServiceLinks: false` on the chaos-mesh workloads (applied in its generate-manifests.sh). Any future package that decodes env strictly needs the same. |
| dapr/keda objects (`Deployment/dapr-operator`, `Service/keda-operator`, …) | open-function, **dapr**, **keda** | open-function vendors whole sub-stacks the platform already runs. Disabled. |
| `Deployment/workflow-controller`, `ConfigMap/workflow-controller-configmap`, `ServiceAccount/argo`, `argoproj.io` CRDs | kubeflow, **argo-workflows** | RESOLVED — kubeflow's generator strips its bundled Argo Workflows control plane and runs on the shared controller. |
| `ServiceAccount/minio-sa` | mimir, **minio** | mimir bundles its own MinIO. |
| `ClusterSecretStore/vault`, `Service/vault` | **openbao**, vault | MUTUALLY EXCLUSIVE — exactly one secrets backend may be enabled. Production enables `openbao`; `vault` is disabled. See below. |
| `DaemonSet/node-agent` | kubescape, velero | Both enabled — pre-existing. |
| `ConfigMap/adhar-dashboard-external-dns`, `adhar-dashboard-opensearch` | kube-prometheus, external-dns / opensearch | Same dashboard shipped twice; cosmetic. |

**A name a package hardcodes in its binary cannot be fixed by renaming the
manifest** — a renamed object is simply never read or populated. When two
packages hardcode the same name they are mutually exclusive in one namespace.

### cosign ↔ buildpack (resolved: separate namespaces)

Both controllers use the knative webhook framework, which reconciles a Secret
named `webhook-certs` in `SYSTEM_NAMESPACE` and populates it with a serving cert
whose SANs cover **its own** Service (`webhook.<ns>.svc` for cosign,
`kpack-webhook.<ns>.svc` for kpack). Whichever controller writes first wins and
the other serves a cert the API server rejects, so one admission webhook fails
TLS. Neither name is configurable, and renaming the manifest does not help: the
binary keeps looking for `webhook-certs` (the earlier `buildpack-webhook-certs`
rename left kpack's webhook crash-looping with `secret "webhook-certs" not
found`). So buildpack keeps `kpack-system` and cosign lives in `adhar-system`;
Tekton pins `WEBHOOK_PORT` explicitly, so cosign's `Service/webhook` service
link in adhar-system is harmless.

### vault ↔ openbao (unresolved by design: enable exactly one)

[OpenBao](https://openbao.org) is the Linux Foundation / OpenSSF fork of
HashiCorp Vault, released under MPL-2.0 rather than Vault's BUSL-1.1. It is
wire-compatible with Vault's HTTP API, and the platform deliberately keeps every
downstream name identical so consumers never have to know which backend is
running:

| Name | Owned by | Why it is shared |
|---|---|---|
| `ClusterSecretStore/vault` | both packages | Consumers reference the store **by name** (`adhar-ai/manifests/llm-secret-external.yaml`, `vllm/manifests/hf-token.yaml`). Renaming it would silently wedge each of their ExternalSecrets in `SecretSyncedError`. The External Secrets provider is `vault:` either way. |
| `Service/vault` | vault (from its chart) / openbao (`manifests/vault-compat.yaml`) | Consumers address the backend by DNS name: `core/adhar-console` (`VAULT_URL`) and `security/credential-rotation` (break-glass write). |
| `vault_*` Prometheus metrics | both | OpenBao keeps Vault's metric namespace, so `observability/kube-prometheus/manifests/dashboard-vault.yaml` works for either. |
| `MutatingWebhookConfiguration/*-agent-injector-cfg` | both (distinct names) | Two agent injectors in one namespace would both mutate pods; only one belongs. |

Because both packages define `ClusterSecretStore/vault` and `Service/vault`,
enabling both makes two ArgoCD Applications claim the same objects: they flap
between OutOfSync and Synced and overwrite each other every sync, and the store
alternates between pointing at `vault` and at `openbao`. **Enable exactly one.**

- `adhar-appset-production.yaml` / `environments/production/config.yaml`:
  `openbao` enabled, `vault` disabled.
- `adhar-appset-local.yaml` / `environments/local/config.yaml`: both disabled
  (a single Kind node does not need a secrets backend).

Switching backends is not a data migration: OpenBao starts on empty `file`
storage under `/openbao/data`. Export from Vault and re-import before flipping
the flags — see `security/openbao/README.md`.

## 2. Service-link environment variable collisions

Kubernetes injects `<SERVICE_NAME>_PORT` env vars into every pod for every
Service in the same namespace. With all packages sharing one namespace, a
generically-named Service can hijack an unrelated component's configuration,
because many controllers read their flags from env vars of exactly that shape.

Observed: cosign's policy-controller ships `Service/webhook`, which sets
`WEBHOOK_PORT=tcp://<clusterIP>:443` on every pod in `adhar-system`. Crossplane
reads `WEBHOOK_PORT` as its `--webhook-port` flag and crashlooped with
`expected a valid 64 bit int`. The Service name is hardcoded in the
policy-controller binary (it is the cert SAN), so the fix is on the consumer
side: crossplane sets `enableServiceLinks: false`.

Generically-named Services in the stack: `webhook` (cosign), `controller`
(buildpack, open-function), `mysql` / `cache-server` / `ml-pipeline` (kubeflow),
`operator` / `storage` (kubescape), `proxy` (jupyterhub).

**Set `enableServiceLinks: false` on any platform component that reads
configuration from env vars** (or set the variable explicitly — an explicit
container env always wins over an injected service link). This is the standing
ADR-0011 rule.

## Checking for new collisions

```bash
cd platform/stack/packages
python3 - <<'PYSCAN'
import glob, re, collections
owners = collections.defaultdict(set)
for f in glob.glob('*/*/manifests/**/*.yaml', recursive=True):
    pkg = f.split('/')[1]
    for doc in open(f, errors='ignore').read().split('\n---'):
        k = re.search(r'^kind:\s*(\S+)', doc, re.M)
        n = re.search(r'^metadata:\s*\n(?:\s+\S.*\n)*?\s+name:\s*(\S+)', doc, re.M)
        if k and n and k.group(1) in ('Service','Deployment','StatefulSet',
                                      'DaemonSet','ConfigMap','Secret','ServiceAccount'):
            owners[(k.group(1), n.group(1).strip('"\''))].add(pkg)
for (kind, name), pkgs in sorted({k: v for k, v in owners.items() if len(v) > 1}.items()):
    print(f'{kind}/{name}: {", ".join(sorted(pkgs))}')
PYSCAN
```

## Related invariants

- **Packages must never ship a `kind: Namespace` object.** An app that tracks
  `Namespace/adhar-system` will delete the entire platform namespace when it is
  pruned, and a Namespace carrying `pod-security.kubernetes.io/enforce:
  restricted` blocks pod creation platform-wide. kpack and Kubeflow Pipelines
  both vendor one; both generators drop it.
- **Never bundle a capability the platform already provides.** Kubeflow
  Pipelines shipped its own MinIO *and* its own Argo Workflows; both are stripped
  at generation time so it consumes the platform's.
- **Namespace references hide in env var values, CLI flags and ConfigMap data**,
  not just `namespace:` fields. A stale `OPERATOR_NAMESPACE: trivy-system`
  survived the consolidation sweep and crashlooped trivy-operator; kpack's
  `CONFIG_LOGGING_NAME` kept pointing at `config-logging` after the ConfigMap was
  renamed, which would have made it read tekton's. Grep for `value: <name>-system`,
  `--*namespace=` and `.<old-namespace>:` when adding packages.
