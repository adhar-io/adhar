# Accessing a Production (Cloud / On-Prem) Adhar Platform

This guide covers how you reach the platform after `adhar up` provisions a
**production** cluster (DigitalOcean, AWS, GCP, Azure, Civo, or a custom
on-prem cluster). It applies to both provisioning modes — the default
**kubeadm-on-raw-compute** control plane and the opt-in managed-Kubernetes
mode (`useManagedK8s: true`) — because everything below is identical in both.

> Local development (`adhar up` with the Kind provider) is different: it uses
> `*.adhar.localtest.me:8443`, which resolves to your own machine. Nothing in
> this document uses `localtest.me` — production clusters use your real domain.

## 1. Your real domain drives every URL

Set the base domain once in your config:

```yaml
globalSettings:
  defaultHost: platform.adhar.io      # your domain, managed by your DNS provider
  defaultHttpsPort: 443
  email: admin@platform.adhar.io      # Let's Encrypt registration
```

Every platform hostname derives from it — there is **no hardcoded host or
port** anywhere in the foundation or the GitOps stack:

| Service  | URL                                  |
|----------|--------------------------------------|
| Console  | `https://console.<defaultHost>`      |
| ArgoCD   | `https://argocd.<defaultHost>`       |
| Gitea    | `https://gitea.<defaultHost>`        |
| Keycloak | `https://keycloak.<defaultHost>`     |
| Nexus    | `https://nexus.<defaultHost>`        |
| Harbor   | `https://harbor.<defaultHost>`       |

Behind a cloud LoadBalancer the standard port 443 is used, so URLs carry no
explicit port and OIDC issuers (e.g. `https://keycloak.<defaultHost>/realms/adhar`)
match exactly.

## 2. kubeconfig is set up for you — nothing to fetch

When `adhar up` finishes bootstrapping a production cluster it **automatically**:

1. saves the cluster's kubeconfig to
   `~/.adhar/clusters/<cluster>/kubeconfig` (mode `0600`, next to the
   cluster's SSH key), and
2. merges it into your default kubeconfig (`~/.kube/config`) as the context
   **`adhar-<cluster>`** and makes it the **current** context.

So on the machine that ran `adhar up`, `kubectl` and the `adhar` CLI already
point at the new cluster:

```bash
kubectl config current-context      # adhar-dev
kubectl get nodes
```

To use the standalone file instead (e.g. from a script, CI, or another shell
that has its own `KUBECONFIG`):

```bash
export KUBECONFIG=~/.adhar/clusters/dev/kubeconfig
```

Re-running `adhar up` for the same cluster **replaces** the `adhar-<cluster>`
entries rather than duplicating them.

## 3. Getting credentials — the proper way

Never scrape secrets out of pods or YAML. The CLI reads the platform's
labelled credential secrets from the cluster your kubeconfig points at:

```bash
adhar get secrets                 # all platform credentials
adhar get secrets -p argocd       # a single package, e.g. the ArgoCD admin login
adhar get secrets -p gitea
adhar get secrets -p keycloak
```

Because the context is already current after `adhar up`, no `export KUBECONFIG`
is needed on that machine. From elsewhere, point `KUBECONFIG` at the standalone
file (section 2) first.

The **Console** itself uses **Keycloak SSO** (realm `adhar`) — log in at
`https://console.<defaultHost>` with your Keycloak user; you don't paste
secrets into it.

### Rotating the bootstrap credentials

The day-0 `gitea_admin` and ArgoCD `admin` passwords are well-known bootstrap
values. In production enable the `credential-rotation` package, which rotates
both to strong random values (written to Vault as break-glass copies when Vault
is installed) and keeps `adhar get secrets` truthful.

## 4. DNS and TLS (fully automatic)

Nothing in the stack names a domain, an email or a cloud. `adhar up` records
the edge configuration on the `AdharPlatform` spec (`host`, `email`,
`dnsProvider`, `clusterName`) and renders the stack templates
(`*.yaml.tmpl` under `platform/stack`) and the cloud Gateway from it when the
GitOps repositories are seeded:

- **DNS provider** — derived from the environment's cloud (DigitalOcean DNS,
  Route53, Cloud DNS, Azure DNS, Civo DNS). Override with
  `globalSettings.dnsProvider` (`cloudflare` for on-prem/custom clusters,
  `none` to disable). The DNS zone must be `<defaultHost>` (e.g.
  `platform.adhar.io`), delegated to that provider.
- **Credentials** — the CLI materialises the provider's credentials (from the
  `providers.<name>` block or the provider's usual environment variables) into
  the `adhar-dns-provider` Secret in `adhar-system`. They never enter Git.
- **DNS — external-dns** runs with `--provider=<dnsProvider>
  --domain-filter=<defaultHost> --txt-owner-id=adhar-<cluster>` and publishes a
  record for every platform HTTPRoute hostname (`console.<defaultHost>`,
  `argocd.<defaultHost>`, …) pointing at the Cilium Gateway's LoadBalancer IP.
  Without a DNS provider it stays inert (`inmemory`).
- **TLS — cert-manager** issues the wildcard `*.<defaultHost>` from Let's
  Encrypt through the DNS-01 `adhar-letsencrypt-dns` ClusterIssuer (rendered
  with the provider's solver), requested by the Gateway's
  `cert-manager.io/cluster-issuer` annotation and stored in the `adhar-cert`
  Secret the HTTPS listener already references. Without a DNS-01 capable
  provider the Gateway keeps `adhar-selfsigned` (browser warning, same as
  local). `globalSettings.email` is the ACME account address.
- **Ingress — Cilium Gateway API** fronts everything through a single cloud
  LoadBalancer (ports 80/443).

```yaml
globalSettings:
  defaultHost: platform.adhar.io      # your delegated zone
  email: admin@platform.adhar.io      # ACME registration
  # dnsProvider: cloudflare           # only when it differs from the cloud
```

Day-2: re-running `adhar up` against a live cluster adopts its machines but
does not rewrite what is already seeded. To roll out changed edge settings
or a newer stack, run `adhar upgrade --yes` (re-renders the foundation — the
Gateway issuer included — and pushes the re-rendered stack to Gitea; use
`--diff-only` to preview). For a clean cluster use
`adhar up -f config.yaml --env <name> --recreate` (deletes the environment's
cluster first, on every provider).

While DNS propagates or before cert-manager has issued a certificate, you can
still reach any service directly through the cluster:

```bash
kubectl port-forward -n adhar-system svc/argo-cd-argocd-server 8443:443   # https://localhost:8443
kubectl port-forward -n adhar-system svc/adhar-console        3000:3000  # http://localhost:3000
```

## 5. Quick checklist after `adhar up`

```bash
kubectl config current-context                      # adhar-<cluster>
adhar get secrets -p argocd                         # credentials
kubectl get gateway -n adhar-system                 # LoadBalancer IP (ADDRESS)
dig +short console.<defaultHost>                    # should resolve to that IP
curl -sI https://console.<defaultHost> | head -1    # HTTP/2 200
```
