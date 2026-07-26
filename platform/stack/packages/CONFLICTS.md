# Package conflicts in the shared `adhar-system` namespace

Every platform package deploys into `adhar-system`. That keeps operations simple,
but it removes the namespace boundary upstream charts assume, which produces two
classes of conflict. Both are checked by the collision scan at the bottom of this
file — run it before enabling a new package.

## 1. Object name collisions

Two packages that define the same `kind/name` will fight: each one's ArgoCD
Application claims ownership, so they flap between OutOfSync and Synced and
overwrite each other's content on every sync.

| Object | Packages | Impact |
|---|---|---|
| `ConfigMap/config-logging` | knative, **tekton** | Knative and Tekton both hardcode this name. |
| `ConfigMap/config-observability` | knative, **tekton** | Same. |
| `ConfigMap/config-defaults` | open-function, **tekton** | open-function vendors Knative's copy. |
| `ConfigMap/config-tracing` | open-function, **tekton** | Same. |
| `Secret/webhook-certs` | cosign, **tekton** | Whichever syncs last owns the cert; the other's admission webhook then fails TLS. |
| `ServiceAccount/minio-sa` | mimir, **minio** | mimir bundles its own MinIO. |

Bold packages are enabled in the local core set, so the conflict is triggered by
enabling the *other* package.

**These cannot be fixed by renaming.** Knative and Tekton both read these
ConfigMaps by fixed name — that is precisely why upstream installs them in
separate namespaces (`knative-serving`, `tekton-pipelines`). Renaming breaks the
package instead of fixing the platform.

Treat the pairs above as mutually exclusive: do not enable `knative` or
`open-function` alongside `tekton`, `cosign` alongside `tekton`, or `mimir`
alongside `minio`, without first giving one of them its own namespace.

## 2. Service-link environment variable collisions

Kubernetes injects `<SERVICE_NAME>_PORT` env vars into every pod for every
Service in the same namespace. With all packages sharing one namespace, a
generically-named Service can hijack an unrelated component's configuration,
because many controllers read their flags from env vars of exactly that shape.

Observed: cosign's policy-controller ships `Service/webhook`, which sets
`WEBHOOK_PORT=tcp://<clusterIP>:443` on every pod in the namespace. Crossplane
reads `WEBHOOK_PORT` as its `--webhook-port` flag and crashlooped with
`expected a valid 64 bit int`. Fixed by setting `enableServiceLinks: false` on
the crossplane Deployments.

Known risk (unrenamable): the jenkins-x package's Lighthouse chart hardcodes
`Service/hook` (its webhook receiver), which injects `HOOK_PORT` into every
service-linked pod in the namespace. No collision has been observed yet;
components that parse `*_PORT`-shaped env vars must set
`enableServiceLinks: false` (the standing ADR-0011 rule).

Generically-named Services currently in the stack — `webhook` (cosign,
buildpack), `controller` (buildpack, open-function), `operator` / `storage`
(kubescape), `proxy` (jupyterhub), `dashboard` (devtron).

**Set `enableServiceLinks: false` on any platform component that reads
configuration from env vars.** Service links are almost never used and disabling
them removes this entire failure class.

## Checking for new collisions

```bash
cd platform/stack/packages
python3 - <<'EOF'
import glob, re, collections
owners = collections.defaultdict(set)
for f in glob.glob('*/*/manifests/*.yaml'):
    pkg = f.split('/')[1]
    for doc in open(f, errors='ignore').read().split('\n---'):
        k = re.search(r'^kind:\s*(\S+)', doc, re.M)
        n = re.search(r'^metadata:\s*\n(?:\s+\S.*\n)*?\s+name:\s*(\S+)', doc, re.M)
        if k and n and k.group(1) in ('Service','Deployment','StatefulSet',
                                      'DaemonSet','ConfigMap','Secret','ServiceAccount'):
            owners[(k.group(1), n.group(1).strip('"\''))].add(pkg)
for (kind, name), pkgs in sorted({k: v for k, v in owners.items() if len(v) > 1}.items()):
    print(f'{kind}/{name}: {", ".join(sorted(pkgs))}')
EOF
```

## Related invariants

- **Packages must never ship a `kind: Namespace` object.** An app that tracks
  `Namespace/adhar-system` will delete the entire platform namespace when it is
  pruned, and a Namespace carrying `pod-security.kubernetes.io/enforce:
  restricted` blocks pod creation platform-wide.
- **Namespace references hide in env var values and CLI flags**, not just
  `namespace:` fields. A stale `OPERATOR_NAMESPACE: trivy-system` survived the
  consolidation sweep and crashlooped trivy-operator. Grep for
  `value: <name>-system` and `--*namespace=` when adding packages.
