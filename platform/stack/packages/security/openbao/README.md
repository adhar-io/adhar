# OpenBao (Adhar secret backend)

[OpenBao](https://openbao.org) is the Linux Foundation / OpenSSF fork of
HashiCorp Vault, released under **MPL-2.0** (Vault itself moved to BUSL-1.1).
It is the platform secret backend in the **production** profile, wired to
[external-secrets](../external-secrets/) so workloads consume OpenBao secrets as
native Kubernetes `Secret`s.

OpenBao is wire-compatible with Vault's HTTP API. Consequences that matter:

- the External Secrets provider is still `vault:`;
- the `ClusterSecretStore` is still **named `vault`** — every consumer
  (`adhar-ai`, `vllm`, …) keeps working with no change;
- a compatibility `Service/vault` is published so consumers that address the
  backend by DNS name (`adhar-console`'s `VAULT_URL`,
  `credential-rotation`'s break-glass write) keep working too;
- Prometheus metric names are unchanged (`vault_*`), so the existing
  `dashboard-vault` Grafana dashboard keeps working.

Because of those shared names, **openbao and vault are mutually exclusive** —
exactly one secrets backend may be enabled (see
[`../../CONFLICTS.md`](../../CONFLICTS.md)).

Deployed as a single-node, persistent **standalone** server (see
`values.yaml`). The UI is exposed through the Cilium Gateway API
(`manifests/httproute.yaml`) at `https://openbao.<host>` — no nginx Ingress.

## Versions

| | |
| --- | --- |
| Helm chart | `openbao/openbao` **0.29.4** (`https://openbao.github.io/openbao-helm`) |
| App version | OpenBao **v2.6.2** (`quay.io/openbao/openbao:2.6.2`) |
| License | MPL-2.0 |

`generate-manifests.sh` passes `--kube-version 1.32.0` because the chart declares
`kubeVersion: >= 1.30.0-0`, which `helm template` cannot infer offline.

## Layout

| File | Purpose |
| ---- | ------- |
| `values.yaml` | Helm chart values (standalone + persistence + UI + telemetry). |
| `manifests/install.yaml` | Rendered chart (`generate-manifests.sh`). |
| `manifests/bootstrap.yaml` | Auto-init / unseal / configure Job (waves 1–2). |
| `manifests/openbao-clustersecretstore.yaml` | ESO `ClusterSecretStore/vault` (wave 3). |
| `manifests/oidc.yaml` | Keycloak client ConfigMap + OIDC/CA ExternalSecrets. |
| `manifests/httproute.yaml` | Gateway API route for the UI. |
| `manifests/vault-compat.yaml` | `Service/vault` alias for DNS-name consumers. |
| `manifests/servicemonitor.yaml` | Prometheus scrape of `/v1/sys/metrics` (wave 10). |

## Bootstrap

`manifests/bootstrap.yaml` runs an idempotent `openbao-bootstrap` Job (ArgoCD
sync-wave `2`, after the chart objects at wave `0` and the bootstrap RBAC/config
at wave `1`). The Job:

1. Waits for `openbao-0` (svc `openbao`) to answer `bao status`.
2. Initializes OpenBao (`-key-shares=1 -key-threshold=1`) **only if it reports
   `initialized: false`**, and stores the init JSON (unseal key + root token) in
   the `openbao-keys` Secret (`adhar-system`). It never re-initialises and never
   prints the root token.
3. Unseals using the stored key (skips if already unsealed).
4. Logs in with the root token and idempotently configures:
   - KV v2 secrets engine at `secret/`
   - `kubernetes` auth method, configured from the in-cluster SA token / CA /
     API host (OpenBao's own pod SA is the token reviewer via its
     `system:auth-delegator` binding)
   - policy `external-secrets` (read on `secret/data/*` + `secret/metadata/*`)
   - kubernetes auth role `external-secrets` bound to the external-secrets
     controller ServiceAccount (`external-secrets` / `adhar-system`)
   - OIDC auth against Keycloak plus `platform-admin` / `platform-developer` /
     `platform-viewer` group→policy mappings (best effort; retried on the next
     sync until the Keycloak client secret and platform CA exist)

Every step is re-runnable; the Job uses `restartPolicy: OnFailure` and runs as an
ArgoCD `Sync` hook so configuration drift is reconciled on every sync.

The chart's default readiness probe (`bao status`) exits non-zero while the
server is sealed, so the pod would never become Ready and the wave-2 Job would
never run. `values.yaml` therefore switches readiness to
`/v1/sys/health?…&sealedcode=204&uninitcode=204` — verified to return `204` on a
freshly started, uninitialised server.

### Key values (for migrating platform secrets)

| Setting | Value |
| ------- | ----- |
| OpenBao address | `http://openbao.adhar-system.svc.cluster.local:8200` (alias: `http://vault.adhar-system.svc.cluster.local:8200`) |
| KV v2 mount | `secret/` (engine version `v2`) |
| K8s auth mount | `kubernetes/` |
| K8s auth role | `external-secrets` |
| Policy | `external-secrets` (read `secret/data/*`, `secret/metadata/*`) |
| ES controller SA | `external-secrets` (ns `adhar-system`) |
| Init keys Secret | `openbao-keys` (ns `adhar-system`) |
| ClusterSecretStore | `vault` (name kept for compatibility) |

Write secrets under the KV v2 mount, e.g.:

```sh
bao kv put secret/myapp/config username=foo password=bar
```

and reference them from an `ExternalSecret` via the `vault` `ClusterSecretStore`.

## !!! Production hardening !!!

The bootstrap Job stores the **unseal key and root token in a plain Kubernetes
Secret** (`openbao-keys`). That is self-contained and works on every provider,
but a cloud KMS auto-unseal is strictly better where one is available: add a
`seal "awskms" {}` / `seal "azurekeyvault" {}` / `seal "gcpckms" {}` stanza to
`server.standalone.config` in `values.yaml` so OpenBao unseals itself from the
KMS and no unseal material is ever stored in-cluster. In that mode drop the
init/unseal steps of `bootstrap.yaml` (or restrict the Job to the post-init
configuration steps), and protect the root token — revoke it after creating
scoped admin tokens.

## Migrating from the vault package

The two packages are mutually exclusive, and **secrets do not migrate
automatically**: OpenBao starts with an empty `file` storage backend. On a
cluster that already ran Vault with data worth keeping, export first
(`vault kv get -format=json …`) and re-import with `bao kv put …` after OpenBao
is initialised, or point OpenBao at a copy of Vault's `/vault/data` directory —
the storage format is compatible, the mount path is not (`/openbao/data`).
