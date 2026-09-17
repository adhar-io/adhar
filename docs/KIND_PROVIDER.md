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

The engine is used for the whole lifecycle: creating nodes, running the local
image cache, removing leftover containers and the `kind` network on teardown.
Earlier versions shelled out to `docker` for those last steps, so on a
Podman-only machine the cluster came up and then teardown and image preloading
silently did nothing.

### The local image cache

The curated core pulls ~90 images (~10 GB) from eight public registries. A fresh
Kind node has none of them, and the old fix — `docker save` a hand-picked
"core" set on the host and `ctr import` it into the node — moved 7 GB through a
tar on **every** `adhar up` (about three minutes before the first CRD was
installed), still missed two thirds of the images, and its list went stale.

Now `adhar up` runs one small `registry` container per upstream registry
(`adhar-registry-cache-<host>`, image `registry:3.0.0`, all sharing the engine
volume `adhar-registry-cache`) on the `kind` network as a pull-through cache,
and points the node's containerd at them through `certs.d` mirrors with the
upstream as fallback. Nothing is copied by hand:

- **First run** on a machine: images are pulled from the internet through the
  caches, which keep them. Expect the usual pull times.
- **Every later run**: all images come from local disk at LAN speed — the
  Cilium phase, Gitea, Crossplane and the whole GitOps sync no longer wait on
  the network. Tags are re-resolved upstream when reachable, so `:latest`
  images stay current; offline, the cached copy is served.
- **A cache that is down** is skipped by containerd, which falls back to the
  upstream server; it can never break a pull.
- `adhar down` **stops** the caches and keeps the volume; the next `adhar up`
  restarts them. `adhar down --purge-image-cache` (or `make clean-image-cache`)
  removes containers and volume.

Measured on one machine (Docker Desktop, 11 CPUs), `adhar up --recreate`
before and after, warm cache:

| Stage | Before | After |
| --- | --- | --- |
| Kind cluster (create + old 7 GB image save/load) | 3m30s | 37s–1m03s (no save/load; includes deleting the previous cluster) |
| GitOps repos (seed the 62 MB stack into Gitea) | ~60s, hidden inside "Crossplane" | 16s — packed on the host as a git bundle, Gitea's CPU limit raised from 200m to 2 |
| Crossplane | 1m38s | 12s — core Deployment now starts before seeding; readiness polled instead of fixed sleeps |
| Total to "GitOps sync" | > 7 min | 3m24s |

What remains is component readiness (ArgoCD, Gitea, Cilium starting up) and
the ArgoCD sync of the curated core itself.

Only the three Cilium data-path images are still seeded from the host cache
(`make preload-images`), and only while the registry cache is cold — they are
on the critical path before the CNI is up.

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
