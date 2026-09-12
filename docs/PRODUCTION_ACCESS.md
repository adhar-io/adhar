# Accessing a Production Adhar Platform

**What this is for:** reaching the platform after `adhar up` provisions a
**production** cluster (DigitalOcean, AWS, GCP, Azure, Civo, or a custom on-prem
cluster) — URLs, kubeconfig, credentials, DNS/TLS, and a post-install checklist.

It applies to both provisioning modes — the default
**kubeadm-on-raw-compute** control plane and the opt-in managed-Kubernetes mode
(`useManagedK8s: true`) — because everything below is identical in both.

> Local development (`adhar up` with the Kind provider) is different: it uses
> `*.adhar.localtest.me:8443`, which resolves to your own machine. Nothing here
> uses `localtest.me` — production clusters use your real domain.

---

## Contents

1. [Your domain drives every URL](#1-your-domain-drives-every-url)
2. [kubeconfig is set up for you](#2-kubeconfig-is-set-up-for-you)
3. [Getting credentials](#3-getting-credentials)
4. [DNS and TLS are automatic](#4-dns-and-tls-are-automatic)
5. [Checklist after `adhar up`](#5-checklist-after-adhar-up)

---

## 1. Your domain drives every URL

Set the base domain once:

```yaml
globalSettings:
  defaultHost: platform.adhar.io      # your domain, managed by your DNS provider
  defaultHttpsPort: 443
  email: admin@platform.adhar.io      # Let's Encrypt registration
```

Every platform hostname derives from it — there is **no hardcoded host or port**
anywhere in the foundation or the GitOps stack:

| Service | URL |
| --- | --- |
| Console | `https://console.<defaultHost>` |
| ArgoCD | `https://argocd.<defaultHost>` |
| Gitea | `https://gitea.<defaultHost>` |
| Keycloak | `https://keycloak.<defaultHost>` |
| OpenBao (secrets) | `https://openbao.<defaultHost>` |
| Harbor | `https://harbor.<defaultHost>` |
| Nexus | `https://nexus.<defaultHost>` |

Behind a cloud LoadBalancer the standard port 443 is used, so URLs carry no
explicit port and OIDC issuers (e.g.
`https://keycloak.<defaultHost>/realms/adhar`) match exactly.

## 2. kubeconfig is set up for you

When `adhar up` finishes bootstrapping a production cluster it **automatically**:

1. saves the cluster's kubeconfig to `~/.adhar/clusters/<cluster>/kubeconfig`
   (mode `0600`, next to the cluster's SSH key), and
2. merges it into `~/.kube/config` as the context **`adhar-<cluster>`**, and
   makes it the **current** context.

So on the machine that ran `adhar up`, both `kubectl` and the `adhar` CLI
already point at the new cluster:

```bash
kubectl config current-context      # adhar-dev
kubectl get nodes
```

To use the standalone file instead (a script, CI, or another shell with its own
`KUBECONFIG`):

```bash
export KUBECONFIG=~/.adhar/clusters/dev/kubeconfig
```

Re-running `adhar up` for the same cluster **replaces** the `adhar-<cluster>`
entries rather than duplicating them.

## 3. Getting credentials

Never scrape secrets out of pods or YAML. The CLI reads the platform's labelled
credential secrets from the cluster your kubeconfig points at:

```bash
adhar get secrets                 # all platform credentials
adhar get secrets -p argocd       # one package, e.g. the ArgoCD admin login
adhar get secrets -p gitea
adhar get secrets -p keycloak
adhar get secrets --all           # include every package that publishes one
```

Values are printed in full — the table grows to fit the longest secret.

Packages whose UI keeps its own login (Plane, Airbyte, PostHog, …) ship their
generated admin credentials as a Secret labelled `adhar.io/cli-secret=true`
with `adhar.io/package-name=<package>`, so `adhar get secrets -p <package>`
prints them with no per-app code in the CLI.

The **Console** itself uses **Keycloak SSO** (realm `adhar`) — log in at
`https://console.<defaultHost>` with your Keycloak user; you don't paste secrets
into it.

### Rotating the bootstrap credentials

The day-0 `gitea_admin` and ArgoCD `admin` passwords are well-known bootstrap
values. In production enable the `credential-rotation` package: it rotates both
to strong random values, keeps `adhar get secrets` truthful, and writes
break-glass copies to the platform secrets backend at
`secret/adhar/bootstrap-credentials`.

### The secrets backend is OpenBao

The production backend is **OpenBao** (`security/openbao`) — the Linux
Foundation, MPL-2.0 fork of HashiCorp Vault. The `vault` package is still wired
as the alternative but ships **disabled**; exactly one secrets backend may be
enabled ([CONFLICTS.md](../platform/stack/packages/CONFLICTS.md)).

Because OpenBao is API-compatible, **nothing about the secrets path changed for
consumers**:

| Surface | Still |
| --- | --- |
| External Secrets provider | `vault:` |
| `ClusterSecretStore` name | `vault` |
| In-cluster address | `vault.adhar-system.svc.cluster.local:8200` — a compatibility `Service/vault` in `adhar-system` selects the OpenBao server pods |
| Break-glass path | `secret/adhar/bootstrap-credentials` |
| UI | `https://openbao.<defaultHost>` (Keycloak SSO) |

The rotation Job looks for the `openbao-keys` Secret first and falls back to
`vault-keys`, so it works with either backend. The compatibility Service is also
why the two packages are mutually exclusive: the Vault chart renders its own
`Service/vault` into the same namespace, and two ArgoCD Applications owning one
object fight forever.

For production hardening of the backend itself (KMS auto-unseal so no unseal
material stays in-cluster), see
[PRODUCTION §5](PRODUCTION.md#5-security-hardening).

## 4. DNS and TLS are automatic

Nothing in the stack names a domain, an email or a cloud. `adhar up` records the
edge configuration on the `AdharPlatform` spec (`host`, `email`, `dnsProvider`,
`clusterName`) and renders the stack templates (`*.yaml.tmpl` under
`platform/stack`) and the cloud Gateway from it when the GitOps repositories are
seeded.

| Piece | Behaviour |
| --- | --- |
| **DNS provider** | Derived from the environment's cloud (DigitalOcean DNS, Route53, Cloud DNS, Azure DNS, Civo DNS). Override with `globalSettings.dnsProvider` — `cloudflare` for on-prem/custom, `none` to disable. The zone must be `<defaultHost>`, delegated to that provider |
| **Credentials** | The CLI materialises the provider's credentials (from the `providers.<name>` block or its usual environment variables) into the `adhar-dns-provider` Secret in `adhar-system`. They never enter Git |
| **external-dns** | Runs with `--provider=<dnsProvider> --domain-filter=<defaultHost> --txt-owner-id=adhar-<cluster>` and publishes a record for every platform HTTPRoute hostname pointing at the Cilium Gateway's LoadBalancer IP. Without a DNS provider it stays inert (`inmemory`) |
| **cert-manager** | Issues the wildcard `*.<defaultHost>` from Let's Encrypt through the DNS-01 `adhar-letsencrypt-dns` ClusterIssuer, requested by the Gateway's `cert-manager.io/cluster-issuer` annotation and stored in the `adhar-cert` Secret the HTTPS listener already references. Without a DNS-01-capable provider the Gateway keeps `adhar-selfsigned` (browser warning, same as local) |
| **Ingress** | Cilium Gateway API fronts everything through a single cloud LoadBalancer (ports 80/443) |

```yaml
globalSettings:
  defaultHost: platform.adhar.io      # your delegated zone
  email: admin@platform.adhar.io      # ACME registration
  # dnsProvider: cloudflare           # only when it differs from the cloud
```

**Day-2.** Re-running `adhar up` against a live cluster adopts its machines but
does not rewrite what is already seeded. To roll out changed edge settings or a
newer stack:

```bash
adhar upgrade --diff-only      # preview
adhar upgrade --yes            # re-render the foundation (Gateway issuer included) and push the stack
```

For a clean cluster use
`adhar up -f config.yaml --env <name> --recreate` — it deletes that
environment's cluster first, on every provider.

> `adhar up -f config.yaml` provisions **every** environment in the file.
> Always pass `--env <name>`.

While DNS propagates or before a certificate is issued, reach any service
directly through the cluster:

```bash
kubectl port-forward -n adhar-system svc/argo-cd-argocd-server 8443:443   # https://localhost:8443
kubectl port-forward -n adhar-system svc/adhar-console        3000:3000   # http://localhost:3000
```

## 5. Checklist after `adhar up`

```bash
kubectl config current-context                      # adhar-<cluster>
adhar get status                                    # platform conditions + per-package health
adhar get secrets -p argocd                         # credentials
adhar cluster list --file config.yaml               # --file is REQUIRED (see below)
kubectl get gateway -n adhar-system                 # LoadBalancer IP (ADDRESS)
dig +short console.<defaultHost>                    # should resolve to that IP
curl -sI https://console.<defaultHost> | head -1    # HTTP/2 200

# Crossplane can actually provision (adhar get status does NOT check this)
kubectl get clusterproviderconfigs.kubernetes.m.crossplane.io
```

Then log into ArgoCD **through Keycloak**, not as the local admin, and confirm
you see the full application list — the two defects that hide here are HA-only
and both look like a broken cluster
([TROUBLESHOOTING §3.4–3.5](TROUBLESHOOTING.md#34-ha-only-invalid-redirect-url-on-keycloak-login)).

> **`adhar cluster list` requires `--file <config>`.** Without it, it loads the
> default config, queries no provider and reports "No clusters found" — which
> reads like your clusters are gone. `adhar cluster scale`, `cluster upgrade`
> and `cluster delete` take the same config file; note that on
> `cluster delete`, `-f` means `--force`, so spell out `--file` there.

---

**Related**: [Production Guide](PRODUCTION.md) ·
[Provider Guide](PROVIDER_GUIDE.md) · [Troubleshooting](TROUBLESHOOTING.md) ·
[DigitalOcean runbook](DIGITALOCEAN_PRODUCTION.md)
