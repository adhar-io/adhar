# k8sgpt

The k8sgpt-operator runs a k8sgpt scanner that walks the cluster with 22
analyzers every ten minutes — pods, workloads, Services selecting nothing, PVCs
stuck Pending, unattached HTTPRoutes, webhooks pointing at nothing, node
pressure, storage, security posture — and writes one `Result` object per
problem found.

## How it fits the platform

**Its model calls go through agentgateway, as everyone's do.** The scanner
authenticates with a Kubernetes ServiceAccount token (audience `agentgateway`),
which the gateway trusts alongside Keycloak, so every explanation is governed,
budgeted and attributed to `system:serviceaccount:adhar-system:k8sgpt-gateway`
like any other caller. The token is minted by a sync hook Job and lives in the
`k8sgpt-gateway-token` Secret.

**Adhar AI reads the Results.** Its `insights` tool lists them grouped by kind
and namespace; the `incident` and `platform` agents hold that tool and start
wide from it; the live knowledge source indexes the Results as findings; and the
`cluster-insights` chore summarises them daily, grouped by cause. k8sgpt's
`details` field — its own explanation, when its model ran — is carried through
as k8sgpt's and attributed as such, never as Adhar AI's own conclusion.

**Enabled exactly where agentgateway is.** No gateway, no model, no package:
`enabled: "false"` in the local profile, `"true"` in production and gitops.

## The analyzer list is explicit

Without `spec.filters`, k8sgpt runs **only the Pod analyzer** — that is the
schema's default — and a cluster whose pods are fine but whose Services select
nothing, whose PVCs are Pending or whose HTTPRoutes are unattached reports no
problems at all. `manifests/k8sgpt.yaml` lists every analyzer that applies
here. `Log` is left out on purpose: it copies pod log lines into Results, and
Adhar AI fetches logs on demand through its own tool instead — fresher, scoped,
and not stored as objects forever.

## What ships

| File | What it is |
|---|---|
| `manifests/install.yaml` | the operator, rendered from the chart; CRDs on sync wave -5, ServiceMonitor on 10 |
| `manifests/k8sgpt.yaml` | the gateway ServiceAccount, the token-minting Job, and the `K8sGPT` CR |
| `values.yaml` | ServiceMonitor on, dynamic RBAC on, result logging on |

## Reading the results by hand

```bash
kubectl -n adhar-system get results.core.k8sgpt.ai
kubectl -n adhar-system get result <name> -o jsonpath='{.spec.kind}/{.spec.name}: {.spec.error[*].text}'
```

Or ask Adhar AI: *"what is k8sgpt reporting right now?"*

## Regenerating

```bash
./generate-manifests.sh   # bump CHART_VERSION here and `version` in adhar-package.yaml together
```
