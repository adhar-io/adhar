# llm-d

[llm-d](https://llm-d.ai) in standalone mode: an endpoint-picker router (EPP)
with an agentgateway sidecar in front of vLLM model servers, reached by every
caller on the platform by naming a `local/*` model. Router chart
**v0.10.0**, vLLM **v0.29.0**.

```
  adhar-ai / k8sgpt / anyone ──▶ agentgateway (ai/agentgateway)
       model: local/default         │  `local/*` route
                                    ▼
                       llm-d-epp.adhar-system.svc:8000   (this package: router + EPP)
                                    │  llm-d.ai/inferenceServing=true, llm-d.ai/model=adhar-local
                                    ▼
                       llm-d-decode pods                  (one profile: cpu or gpu)
```

## Three elements, one package

| Element | Path | Contents |
|---|---|---|
| `llm-d` | `manifests/` | the router (EPP + agentgateway sidecar), its CRDs, and the `llm-d-decode` Service |
| `llm-d-cpu` | `manifests/cpu/` | model-cache PVC + `vllm/vllm-openai-cpu:v0.29.0` serving `Qwen/Qwen2.5-0.5B-Instruct` — runs on any node, the development proof |
| `llm-d-gpu` | `manifests/gpu/` | model-cache PVC, optional HF-token ExternalSecret, `vllm/vllm-openai:v0.29.0` serving **`Qwen/Qwen3.6-27B-FP8` on one GPU** |

Enable `llm-d` **plus exactly one profile**. Both Deployments are named
`llm-d-decode` and claim the same PVC; enabling both makes two Applications
fight over one object.

The router selects model servers by the pool label `llm-d.ai/model: adhar-local`,
which is deliberately **not** a model name: swapping the profile re-points
nothing — not the router, not agentgateway's `local/*` route, not adhar-ai.

## `local/default` — the secondary model

Each profile serves two names: its real one (`local/Qwen/Qwen3.6-27B-FP8`,
`local/Qwen/Qwen2.5-0.5B-Instruct`) for a caller that wants *that* model, and
the alias **`local/default`**. adhar-ai keeps the hosted key as its primary
provider and names `local/default` as `ADHAR_AI_LLM_SECONDARY_MODEL`: when
the primary fails — unkeyed, down, 5xx after its retries — every agentic
feature (chat, tasks, chores, journeys, rerank, rewrite) answers from here,
and `/healthz` reports which model answered. `ADHAR_AI_LLM_PROVIDER=local`
makes it primary instead. A budget refusal (429) is never retried on the
secondary: a refusal is a decision, not an outage.

## The GPU profile

* **Qwen3.6-27B-FP8** — Apache-2.0, ungated, ~28 GB of FP8 weights. One GPU
  with **≥ 40 GB** (A100-40GB tightly; A100/H100-80GB, L40S-48GB comfortably).
  `--max-model-len 32768` caps the window at what the agent runtime sends; the
  model's 262k would reserve a KV cache no single card holds.
* `--enable-auto-tool-choice --tool-call-parser hermes` for the runtime's
  tool calls; `--reasoning-parser qwen3` and `enable_thinking: false` by
  default, so the loop's 4k-token completions are spent answering, not
  reasoning about which tool to call. A caller that wants reasoning passes
  `chat_template_kwargs: {"enable_thinking": true}`.
* Needs `infrastructure/gpu-operator` (or any device plugin) advertising
  `nvidia.com/gpu` and labelling nodes `nvidia.com/gpu.present=true`; tolerates
  the conventional `nvidia.com/gpu` taint. It runs as a container on a
  `container` GPU node — on a MIG node it would need a slice large enough for
  28 GB, which is `3g.40gb` or bigger.
* First start downloads the weights to the PVC (`startupProbe` allows 40
  minutes); every restart after that is minutes.

## Checking it

```bash
kubectl -n adhar-system get pods -l llm-d.ai/inferenceServing=true
kubectl -n adhar-system port-forward svc/llm-d-decode 8000 &
curl -s localhost:8000/v1/models | jq '.data[].id'     # local/Qwen/..., local/default
curl -s https://ai.<host>/v1/chat/completions -H "Authorization: Bearer $(adhar auth token)" \
  -d '{"model":"local/default","messages":[{"role":"user","content":"hello"}]}'
```

## Regenerating the router

```bash
./generate-manifests.sh   # bump CHART_VERSION here and version/appVersion in adhar-package.yaml together
```
