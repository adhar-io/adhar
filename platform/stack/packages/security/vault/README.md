# Vault (Adhar secret backend)

HashiCorp Vault is the platform secret backend, wired to
[external-secrets](../external-secrets/) so workloads consume Vault secrets as
native Kubernetes `Secret`s.

Deployed as a single-node, persistent **standalone** server (see
`values.yaml`). The UI is exposed through the Cilium Gateway API
(`manifests/httproute.yaml`) at `https://adhar.localtest.me/vault` — no nginx
Ingress.

## Layout

| File | Purpose |
| ---- | ------- |
| `values.yaml` | Helm chart values (standalone + persistence + UI). |
| `manifests/install.yaml` | Rendered chart (`generate-manifests.sh`). |
| `manifests/httproute.yaml` | Gateway API route for the UI. |
| `manifests/bootstrap.yaml` | Auto-init / unseal / configure (sync-wave 1+). |

## Bootstrap (local / dev)

`manifests/bootstrap.yaml` runs an idempotent `vault-bootstrap` Job (ArgoCD
sync-wave `2`, after the chart objects at wave `0` and the bootstrap RBAC/config
at wave `1`). The Job:

1. Waits for `vault-0` (svc `vault`) to answer.
2. Initializes Vault (`-key-shares=1 -key-threshold=1`) if needed and stores the
   init JSON (unseal key + root token) in the `vault-keys` Secret (vault ns).
3. Unseals using the stored key (skips if already unsealed).
4. Logs in with the root token and idempotently configures:
   - KV v2 secrets engine at `secret/`
   - `kubernetes` auth method, configured from the in-cluster SA token / CA /
     API host (Vault's own pod SA is the token reviewer via its
     `system:auth-delegator` binding)
   - policy `external-secrets` (read on `secret/data/*` + `secret/metadata/*`)
   - kubernetes auth role `external-secrets` bound to the external-secrets
     controller ServiceAccount (`external-secrets` / `external-secrets` ns)

Every step is re-runnable; the Job uses `restartPolicy: OnFailure`.

### Key values (for migrating platform secrets)

| Setting | Value |
| ------- | ----- |
| Vault address | `http://vault.vault.svc.cluster.local:8200` |
| KV v2 mount | `secret/` (engine version `v2`) |
| K8s auth mount | `kubernetes/` |
| K8s auth role | `external-secrets` |
| Policy | `external-secrets` (read `secret/data/*`, `secret/metadata/*`) |
| ES controller SA | `external-secrets` (ns `external-secrets`) |
| Init keys Secret | `vault-keys` (ns `vault`) |

Write secrets under the KV v2 mount, e.g.:

```sh
vault kv put secret/myapp/config username=foo password=bar
```

and reference them from an `ExternalSecret` via the `vault` `ClusterSecretStore`.

## !!! Production / cloud !!!

The bootstrap Job stores the **unseal key and root token in a plain Kubernetes
Secret** (`vault-keys`). This is acceptable for local/dev only.

On a cloud platform, **do not use the stored-keys Job**. Instead enable Vault
**auto-unseal with a cloud KMS** in `values.yaml` `server.ha`/`server.standalone`
`config` (a `seal "awskms" {}` / `seal "azurekeyvault" {}` / `seal "gcpckms" {}`
stanza), so Vault unseals itself from the KMS and no unseal material is ever
stored in-cluster. In that mode drop the init/unseal steps of `bootstrap.yaml`
(or restrict the Job to the post-init configuration steps only), and protect the
root token (revoke it after creating scoped admin tokens).
