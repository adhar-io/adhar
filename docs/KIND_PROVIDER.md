# Kind provider — local development

Adhar on a local Kind cluster. This is the default when you run `adhar up` with
no configuration file, and the only path exercised continuously by CI.

> **Status: exercised continuously.** `make e2e` runs a full
> `adhar up` → verify → `adhar down` cycle on Kind. See
> [PROVIDER_GUIDE.md](PROVIDER_GUIDE.md#2-verification-status-what-is-proven-where).

| | |
|---|---|
| Provisioning model | Kind (Kubernetes in containers) on Docker, Podman or nerdctl; Adhar supplies the CNI |
| Kubernetes | `globals.DefaultKubernetesVersion` (v1.37.0) unless pinned |
| Package set | A curated local-safe core (32 of 91), not the full catalogue |

## 1. Requirements

- **A container engine**: Docker 20.10+, Podman 4+, nerdctl or Finch. Any one is
  enough; there is no Docker-specific code path.
- `kubectl` on `PATH`.
- Roughly 8 CPU and 16 GiB free for the curated core.

### Choosing the engine

Adhar follows Kind's own rule, so the engine that creates the cluster is always
the one that manages it:

1. `KIND_EXPERIMENTAL_PROVIDER`, if set, wins — even if that engine is not
   responding, because overriding it is deliberate and a silent substitution
   would be worse than a clear error.
2. Otherwise the first engine installed and answering, in the order
   `docker`, `podman`, `nerdctl`, `finch`, `nerdctl.lima`.

```bash
adhar version                              # reports the engine actually in use
export KIND_EXPERIMENTAL_PROVIDER=podman   # force one
```

The engine is used for the whole lifecycle: creating nodes, preloading images
from the host cache, removing leftover containers and the `kind` network on
teardown. Earlier versions shelled out to `docker` for those last steps, so on a
Podman-only machine the cluster came up and then teardown and image preloading
silently did nothing.

### Podman specifics

- **macOS and Windows**: start the VM first with `podman machine start`. Give it
  enough headroom — `podman machine init --cpus 6 --memory 12288` is a
  reasonable floor for the curated core.
- **Rootless** works. The node containers need the usual rootless prerequisites:
  `/etc/subuid` and `/etc/subgid` entries for your user, and cgroups v2 with the
  delegation controllers enabled.
- **Privileged ports**: a rootless user may not bind 8443. Use
  `adhar up --port 9443` (HTTP derives as 9080) rather than loosening system
  limits.

## 2. Run it

No configuration file is needed.

```bash
make build
./adhar up
```

That creates a Kind cluster named `adhar` with host ports 8080 and 8443 mapped,
disables Kind's default CNI and kube-proxy (Cilium replaces both), and seeds the
GitOps stack from `platform/stack`.

Useful variants:

```bash
./adhar up --port 9443        # HTTPS port; HTTP derives as 9080
./adhar up --recreate         # delete the existing cluster first
./adhar up --dry-run          # show what would happen
./adhar get secrets           # platform passwords
```

Access URLs follow the port you chose:

```
https://argocd.adhar.localtest.me:8443
https://gitea.adhar.localtest.me:8443
https://console.adhar.localtest.me:8443
```

`*.localtest.me` resolves to `127.0.0.1` from public DNS, so nothing needs to be
added to `/etc/hosts` — but a machine with no DNS egress, or with DNS-rebinding
protection, will fail to resolve it.

Teardown:

```bash
./adhar down
```

`adhar down` with no `--file` only ever looks at Kind. That is the right
behaviour here, and the reason a cloud environment needs its configuration file
passed explicitly.

## 3. Differences from a cloud run

| | Kind | Cloud |
|---|---|---|
| Packages enabled | 32 (curated local-safe core) | 76 (full production set) |
| TLS | Self-signed platform certificate | Let's Encrypt wildcard via DNS-01 |
| Ingress | Host port mapping to node ports 30080/30443 | Cloud load balancer |
| Storage | Kind's local-path provisioner | Cloud block storage, with attach limits |
| Autoscaling | Not applicable | Adds and removes real machines |

The curated core exists because the full catalogue does not fit on one Kind
node: it wants ~55 PersistentVolumes and far more memory than a laptop has.
Enabling extra packages locally is possible but is the usual cause of a Kind
cluster that never converges.

## 4. Configuration file (optional)

Kind needs no config file, but it can appear in one alongside cloud providers:

```yaml
providers:
  kind:
    type: kind
    region: local
    primary: false
    config:
      kindPath: kind
      kubectlPath: kubectl
```

Only the two tool paths are read; set them when the binaries are not on `PATH`.

## 5. Related

[Getting started](GETTING_STARTED.md) ·
[Provider guide](PROVIDER_GUIDE.md) ·
[DigitalOcean provider](DIGITALOCEAN_PROVIDER.md) ·
[Troubleshooting](TROUBLESHOOTING.md)
