# Adhar Platform Examples

Working examples of the self-service resources the platform serves. Every
`platform.adhar.io/v1alpha1` object here is a Crossplane v2 composite (XR) whose
contract lives in `platform/controlplane/configuration/xrd/`; they apply as-is to
any cluster created by `adhar up` (`kubectl explain compositedatabase.spec` shows
the full schema).

Two things are true of all of them:

- **They are namespaced.** Create them in your team's namespace; the
  ResourceQuota/tenant-quota guardrails are enforced there.
- **The provider is chosen by label**, under `spec.crossplane.compositionSelector`.
  `provider: local` is the in-cluster implementation (CNPG, RustFS, Tekton,
  Argo CD); on a cloud cluster set it to `aws`/`azure`/`gcp` and the same request
  produces the managed service instead.

## Files

| File | Kind | What you get |
|------|------|--------------|
| `project.yaml` | CompositeProject | A team project: guard-railed namespace, RBAC bound to the team's Keycloak group, Gitea repo |
| `environment.yaml` | CompositeEnvironment | A tiered environment (dev/test/staging/prod) with quotas and default-deny network policy |
| `application.yaml` | CompositeApplication | An Argo CD-managed app promoted through the platform's environments |
| `database.yaml` | CompositeDatabase | PostgreSQL (CNPG locally) with WAL archiving + daily backup to the object store |
| `storage.yaml` | CompositeStorage | An S3 bucket on RustFS with the credential delivered as a Secret |
| `secret.yaml` | CompositeSecret | A secret sourced from OpenBao through External Secrets, with rotation |
| `pipeline.yaml` | CompositePipeline | A Tekton CI pipeline (clone → build → scan → sign → deploy) for a repository |
| `vcluster.yaml` | CompositeCluster | A virtual workload cluster registered as a DataPlane of this hub |
| `preview-environments-appset.yaml` | ApplicationSet | Per-PR preview environments (superseded by the `adhar-preview-environments` package — see the file header) |
| `auth-config.yaml` | — | `adhar auth configure` input (Keycloak realm/roles) |
| `gcp-config.yaml`, `digitalocean-config.yaml` | — | `adhar up -f` cluster configurations for those clouds |

## Walk-through: a team's first service

```bash
# 1. A project. Its namespace, quota, RBAC and repository come from this one object.
kubectl apply -f project.yaml

# 2. Backing services in that namespace.
kubectl apply -f database.yaml
kubectl apply -f storage.yaml
kubectl apply -f secret.yaml

# 3. CI for the repository, then the application itself.
kubectl apply -f pipeline.yaml
kubectl apply -f application.yaml

# Watch them become Ready (the composed resources are listed under each XR).
kubectl -n team-payments get compositeproject,compositedatabase,compositestorage,compositeapplication
kubectl -n team-payments describe compositedatabase payments-db
```

The CLI produces the same objects — `adhar project create`, `adhar database
create`, `adhar bucket create`, `adhar application deploy`, `adhar cluster
create` — so anything shown here can be scripted either way.

## Multi-environment

Environments are objects too (`adhar environment create` makes one); promotion
between them is a Kargo Stage, driven from the Console's Environments page or
`kargo promote` (the Kargo CLI):

```bash
sed 's/payments-staging/payments-prod/; s/tier: staging/tier: prod/' environment.yaml | kubectl apply -f -
```

## Documentation

- [Getting Started](../docs/GETTING_STARTED.md) · [User Guide](../docs/USER_GUIDE.md) · [Architecture](../docs/ARCHITECTURE.md) · [Provider Guide](../docs/PROVIDER_GUIDE.md)
- Control-plane conventions: `platform/controlplane/CONVENTIONS.md`
- Design records: `docs/adr/`

All examples use placeholder names (`acme`, `payments`); replace them with your
organisation and team. Repository URLs use `gitea.cloud.adhar.io` — substitute
your platform host.
