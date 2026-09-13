# vLLM — self-hosted, OpenAI-compatible inference

[vLLM](https://docs.vllm.ai) ([source](https://github.com/vllm-project/vllm), Apache-2.0) serving an
OpenAI-compatible API **inside the platform**, so the AI layer can run on a model the cluster owns
instead of a third-party endpoint. Pinned to **v0.29.0**.

```
                                 ┌─────────────────────────────────────────┐
  agentgateway  ──────────────▶│ vllm.adhar-system.svc.cluster.local:8000 │
  adhar-ai (openai-compatible) │  /v1/models  /v1/chat/completions        │
  your app (any OpenAI SDK)       └─────────────────────────────────────────┘
                                                   ▲
  https://vllm.<host>/v1  ── Cilium Gateway ────────┘   (debugging path, unauthenticated)
```

## Why hand-written manifests and not the upstream chart

vLLM's official chart is the production-stack [`vllm-stack`](https://vllm-project.github.io/production-stack)
chart (0.1.12). It is the wrong fit here on three counts:

1. **GPU-first defaults.** Its `modelSpec` ships `requestGPU: 1`, `requestGPUType: "nvidia.com/mig-4g.71gb"`
   and `runtimeClassName: "nvidia"`. Adhar's default droplets and the Kind node have **no GPUs**, so the
   chart's out-of-the-box render cannot schedule anywhere the platform actually runs. CPU is technically
   reachable (`requestGPU: 0` drops the resource) but is not an upstream-tested path, and the chart's
   default `vllmConfig.v0: "1"` pins the V0 engine, whose metric names were removed in V1.
2. **It bundles what the platform already has** — its own router in front of the engines and its own Redis.
   That is a second routing layer beside the Cilium Gateway and a second cache beside `data/valkey`, which
   [ADR-0011](../../../../../docs/adr/0011-shared-platform-namespace.md) explicitly forbids.
3. **The real requirement is one Deployment, one Service and a PVC.** Two profiles we own are easier to keep
   correct than a chart we would override on every one of the above points.

## Two profiles, mutually exclusive

| Element | Path | Contents |
|---|---|---|
| `vllm` | `manifests/` | model-cache PVC, Service, HTTPRoute, ServiceMonitor, Grafana dashboard, optional HF-token ExternalSecret |
| `vllm-cpu` | `manifests/cpu/` | CPU-profile Deployment — `vllm/vllm-openai-cpu:v0.29.0`, `Qwen/Qwen2.5-0.5B-Instruct` |
| `vllm-gpu` | `manifests/gpu/` | GPU-profile Deployment — `vllm/vllm-openai:v0.29.0`, `Qwen/Qwen2.5-7B-Instruct`, 1× `nvidia.com/gpu` |

Enable `vllm` **plus exactly one profile**. Both Deployments are named `vllm` and are selected by the same
Service, so enabling both makes two ArgoCD Applications fight over one object. ArgoCD does not recurse into
subdirectories, so the `vllm` element never picks up `cpu/` or `gpu/`. (Same one-directory / several-elements
shape as `security/supply-chain-policies`' `audit/` + `enforce/`.)

**All three ship disabled in every profile** (`adhar-appset-{local,production,gitops}.yaml` and the mirrored
`environments/{local,production}/config.yaml`). vLLM is heavy and nothing in the platform depends on it.

### The official CPU image exists

`vllm/vllm-openai-cpu:v0.29.0` is published by the vLLM project itself — a **separate Docker Hub repository**
from the GPU image, built from `Dockerfile.cpu` with a different torch backend. It is a multi-arch manifest
list (linux/amd64 1.84 GB, linux/arm64 0.88 GB), so the same tag works on an Apple-silicon Kind node and on
x86 cloud nodes. The GPU image will not start without an `nvidia.com/gpu` device; the CPU image is not a
fallback mode of it.

## Enabling it

```yaml
# platform/stack/adhar-appset-<env>.yaml  AND  platform/stack/environments/<env>/config.yaml
- name: "vllm"      { enabled: "true" }
- name: "vllm-cpu"  { enabled: "true" }   # or vllm-gpu, never both
```

Commit and push to the Gitea `packages` repo; ArgoCD reconciles. Locally, free headroom first — a Kind node
running the ~30-app core does not have a spare 6Gi.

The GPU profile additionally needs: a GPU node pool, the **NVIDIA device plugin / GPU Operator** advertising
`nvidia.com/gpu`, and nodes carrying the `nvidia.com/gpu.present=true` label the manifest selects on.

## Using it

```bash
# In-cluster — this is the address agentgateway and adhar-ai use
curl http://vllm.adhar-system.svc.cluster.local:8000/v1/models

# From your laptop, through the Gateway
curl -k https://vllm.adhar.localtest.me:8443/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"qwen2.5-0.5b-instruct","messages":[{"role":"user","content":"hello"}]}'
```

Any OpenAI SDK works — point `base_url` at `/v1` and pass any non-empty API key (there is no auth; see below).

## Observability

`manifests/servicemonitor.yaml` scrapes `/metrics` on port **8000** (vLLM has no separate metrics listener),
and `manifests/dashboard.yaml` ships the **Adhar - vLLM Inference** Grafana dashboard (folder `Platform`,
uid `adhar-vllm`): throughput, TTFT, inter-token latency, e2e latency, queue depth, KV-cache utilisation,
prefix-cache hit rate, preemptions and pod CPU/memory.

Every query uses a metric verified to exist in v0.29.0. The V0-engine names are **gone** in the V1 engine —
`vllm:gpu_cache_usage_perc` (→ `vllm:kv_cache_usage_perc`), `vllm:cpu_cache_usage_perc`,
`vllm:num_requests_swapped`, `vllm:avg_generation_throughput_toks_per_s` (→ derive with `rate()`) — and
appear nowhere. There is **no GPU-utilisation panel**: vLLM exports no GPU metrics and Adhar ships no NVIDIA
DCGM exporter, so such a panel would be permanently empty. Install `dcgm-exporter` alongside the GPU profile
if you need VRAM/SM utilisation.

## Security

⚠️ **The HTTPRoute is unauthenticated.** It exists as a debugging / direct-access path; the production
consumer is `agentgateway` over the cluster-internal Service, which is where auth, per-team routing, rate
limiting and token accounting belong. vLLM's own `--api-key` is one shared static string and upstream warns
against relying on it. Before enabling vLLM on an internet-facing platform, either delete
`manifests/httproute.yaml` or front it with the Keycloak oauth2-proxy (see `application/n8n/manifests/sso.yaml`).

## Gated models

Both default models are ungated and Apache-2.0, so no HuggingFace token is needed. For Llama / Gemma /
Mistral, put a token that has accepted the repo licence into the secrets backend (`vault` ClusterSecretStore — OpenBao in production) — Git carries a pointer, never the value
([ADR-0009](../../../../../docs/adr/0009-secrets-eso-vault.md)):

```bash
vault kv put secret/vllm/hf HF_TOKEN="hf_..."
```

`manifests/hf-token.yaml` templates a `default ""`, so a missing entry still materialises the Secret
and the pod starts rather than the sync looking broken.

## Measured (arm64, 2026-09)

CPU profile, `vllm/vllm-openai-cpu:v0.29.0` + `Qwen/Qwen2.5-0.5B-Instruct`. The Kubernetes numbers come from
the local `adhar` Kind cluster (11 cores, 23 GiB, default `local-path` StorageClass); the serving numbers
come from the same image and the same arguments run under the same CPU/memory/shm limits.

| | |
|---|---|
| Image pull | 1 m 34 s for the 882 MB arm64 variant of the multi-arch tag |
| Weight download | 60 s, 954 MB landing on the PVC at `HF_HOME=/data/hf` |
| Weight load | 7.5 s |
| `init engine (profile, create kv cache, warmup model)` | 123 s (first run — includes the torch AOT compile, which is then cached to `/data/.cache/vllm/torch_compile_cache/` because `HOME=/data`) |
| KV cache at `VLLM_CPU_KVCACHE_SPACE=4` | 349,440 tokens → 85× concurrency at the 4096 window |
| KV cache at the shipped `VLLM_CPU_KVCACHE_SPACE=2` | 174,720 tokens → 42.66× concurrency at the 4096 window |
| Resident before the arena is carved out | ~4.5 GiB |
| Steady resident after serving a request | 6.77 GiB of the 8 GiB limit |

Serving, same image/model/args on 4 cores (`/v1/chat/completions`, 41 prompt + 11 output tokens):
TTFT **0.42 s**, inter-token latency **0.065 s** (~15 tok/s), end-to-end **1.07 s**. Good enough to develop
against; not a production serving rate — that is what the GPU profile is for.

**The OOM trap, hit for real:** with a 4 GiB arena the container was **OOMKilled (exit 137)** under the 8Gi
limit, at the "create kv cache" step immediately after the engine finished initialising — no Python
traceback, just a dead container. The arena is allocated *inside* the container memory limit, so
`VLLM_CPU_KVCACHE_SPACE` + weights + ~3.5 GiB of torch runtime must fit under `resources.limits.memory`.
The shipped default is 2 GiB (174,720 tokens, 42.66× concurrency — measured) and settles at 6.77 GiB of the
8 GiB limit. **Raise the arena and the memory limit together, never one alone.**

**The OpenMP trap:** upstream's `VLLM_CPU_OMP_THREADS_BIND=auto` pins threads to the cores vLLM can *see*,
which is the node's core count, not the container's CPU quota — observed as
`core ids=[0,1,2,3,4,5,6,7,8,9] reserved_cpus=[10]` inside a 4-CPU limit, which CFS then throttles hard.
The CPU profile ships `nobind` plus `OMP_NUM_THREADS` wired to `limits.cpu` through the downward API, so the
thread count tracks the limit automatically and placement is left to the (cgroup-aware) kernel.

## Operating notes

- **Model weights live on the PVC** (`HF_HOME=/data/hf`), so a restart is a disk read, not a re-download.
  The volume is `ReadWriteOnce` and the Deployment strategy is therefore `Recreate` — a `RollingUpdate` can
  never schedule the replacement pod while the old one holds the volume.
- **Startup is slow.** A cold start downloads the checkpoint (~1 GB CPU profile, ~15 GB GPU profile) and then
  loads it. The `startupProbe` allows 20 minutes (CPU) / 30 minutes (GPU) before liveness takes over; without
  it the liveness probe kills the pod mid-load and it crash-loops forever without ever finishing a start.
- **KV cache sizing.** On CPU the cache is ordinary RAM (`VLLM_CPU_KVCACHE_SPACE`, GiB) and vLLM refuses to
  start if `--max-model-len` does not fit in it. On GPU it is VRAM (`--gpu-memory-utilization`). Raise the
  window and the cache together.
- **Sustained `vllm:num_preemptions_total`** means the KV cache is full and requests are being evicted from
  the running batch: lower concurrency or `--max-model-len`, or give the engine more cache.
- **`--tensor-parallel-size` must equal the `nvidia.com/gpu` count** in the pod, or vLLM asserts and refuses
  to start.
