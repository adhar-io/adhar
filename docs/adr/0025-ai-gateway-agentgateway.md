# ADR-0025: agentgateway as the platform's AI data plane

**Status**: Accepted (package `ai/agentgateway` — chart v1.5.0 — wired into all three ApplicationSets, opt-in/disabled by default, enabled exactly where `adhar-ai` is enabled) · **Date**: 2026-09

## Context

[ADR-0024](0024-agentic-ai-platform.md) gave Adhar an agentic control layer and, with it, three pieces of AI *plumbing* written as first-party application code:

- **`adhar-ai-llm-gateway`** — a Deployment fronting whichever LLM provider the single `adhar-ai-llm` Secret selects, enforcing token/rate budgets from `BUDGET_*` environment variables.
- **Seven MCP servers**, each independently validating an OIDC token (`OIDC_ISSUER_URL` / `OIDC_CLIENT_ID` on every Deployment) before serving its tools.
- **Two public hostnames** (`ai.<host>`, `mcp.<host>`) routed to the agent runtime, which fronts those seven servers.

That design was right for ADR-0024's question — *how does an agent act safely?* — and is wrong for the question that followed it: *who governs the traffic?* The plumbing has four structural problems, and none of them are bugs in the implementation:

1. **The governance controls are in seven places, in Python.** Token validation, group checks and budget accounting are re-implemented per service. Seven implementations of one security control is seven chances to drift, and there is no single point at which a reviewer can answer "what is enforced on an AI request?"
2. **The controls that matter most do not exist at all.** The `adhar-ai-llm-gateway` image is unwritten (`ghcr.io/adhar-io/adhar-ai-gateway:latest` is listed in the package README as "does not yet exist"). Building it means writing, from scratch, prompt/response content filtering, per-identity token budgets, MCP multiplexing, OTel span emission with GenAI semantic conventions, and a model cost catalog — a multi-quarter project that is not differentiated platform work.
3. **Clients see seven MCP endpoints.** Federating them is the agent runtime's job today, which puts an application process on the critical path of every tool call and means an external client (Claude Code, an IDE, ChatOps) cannot reach the governed tools without going through Adhar-specific code.
4. **AI traffic is invisible.** No token counters, no cost attribution, no per-model latency — exactly the data a platform team needs before letting an agent loose, and exactly what a bespoke gateway would have to grow.

Meanwhile the ecosystem produced a component for this shape of problem. **[agentgateway](https://agentgateway.dev)** (Apache 2.0; contributed by Solo.io to the Linux Foundation in August 2025, now under the Agentic AI Foundation) is a Rust data plane that speaks HTTP, LLM, MCP and A2A natively, configured through Kubernetes Gateway API plus four CRDs, and it implements every control in the list above as configuration.

Alternatives considered:

- **Finish building `adhar-ai-llm-gateway`.** Keeps everything first-party and exactly shaped to Adhar. Costs a multi-quarter build of undifferentiated proxy features, then permanent maintenance of a security-critical component tracking a fast-moving ecosystem (the MCP spec revised twice in 2026 alone). Rejected: this is the "never bundle a capability someone else maintains better" trap, in a domain where being behind the spec is a security problem.
- **LiteLLM as the LLM proxy.** Mature, broad provider support, widely used. But it is an LLM proxy only — it has no MCP federation, no A2A, no Gateway API integration, and no tool-level authorization, so the MCP half of ADR-0024 would still need a second component and a second policy language. Rejected: solves half the problem and adds a runtime.
- **Envoy AI Gateway.** Gateway API native and CNCF-adjacent. Weaker MCP story at the time of writing (MCP is the protocol most of Adhar's AI surface actually speaks), and it would introduce a second Envoy control plane alongside Cilium's. Reconsider if the MCP gap closes.
- **Put the controls in Cilium's Envoy at the existing edge.** No new component. But Cilium's Gateway implementation is protocol-agnostic: it cannot count tokens, parse a `tools/call`, or run a prompt guard, because those require understanding LLM and MCP payloads. Rejected: the capability does not exist and is not Cilium's job.
- **agentgateway as a dedicated AI data plane behind the Cilium edge.** Chosen.

## Decision

Adopt **agentgateway v1.5.0** as the platform's AI data plane, as the package `platform/stack/packages/ai/agentgateway/`, and retire the bespoke `adhar-ai-llm-gateway`.

**1. One governed endpoint for each AI protocol.** Every AI request in the platform — from the `adhar ai` CLI, the Console chat, the agent runtime, the MCP servers themselves, or an external IDE — traverses one proxy that understands what it is carrying:

| Surface | URL | What it is |
|---|---|---|
| LLM | `https://ai.<host>/v1/chat/completions` | OpenAI-compatible; the caller names a **model**, never a provider |
| MCP | `https://mcp.<host>/mcp` | All seven `adhar-ai` tool servers federated into one endpoint |

**2. Model-name routing across hosted and self-hosted models.** A `PreRouting` policy lifts `.model` out of the JSON body into an `x-model` header; ordinary Gateway API header matches then select a backend: `claude-*` → Anthropic, `gpt-*`/`o[1-9]-*` → OpenAI, `local/*` → the in-cluster vLLM (`ai/vllm`) at `vllm.adhar-system.svc.cluster.local:8000`. This uses only stable primitives — the native `AgentgatewayModel` API is left disabled because upstream marks it experimental. The vLLM backend is addressed by DNS **name**, not by a Service `backendRef`, so that `ai/vllm` being absent degrades to a 503 on `local/*` requests instead of a dangling reference that would report the whole Application Degraded.

**3. Credentials stay where they are.** Keys come from the existing `adhar-ai-llm` Secret (Vault → ESO), read server-side by the proxy and never returned to a caller or placed in a model context. ADR-0024's "one secret turns agency on" property is preserved exactly; what changes is that the guarantee is now enforced by the data plane rather than by application code.

**4. Identity and authorization move to the data plane, once.** A single `jwtAuthentication` policy on the Gateway validates Keycloak tokens for **every** route — LLM and MCP alike. The issuer is the public realm URL (it must match the `iss` claim byte-for-byte), while the JWKS is fetched from the **in-cluster** `keycloak:8080` Service so key retrieval works before DNS, TLS and the edge have settled on a fresh cluster, and so key rotation never depends on the platform's own ingress. Authorization is two merged `Allow` rules over the platform's existing Keycloak groups:

| Group | LLM completions | MCP read tools (`cluster`, `observability`, `cost`) | MCP write tools (`gitops`, `provision`, `security`, `catalog`) |
|---|---|---|---|
| `platform-developer` | ✅ | ✅ | ❌ |
| `platform-admin` | ✅ | ✅ | ✅ |
| `platform-viewer` / none | ❌ | ❌ | ❌ |

The same groups already drive Gitea teams and Kubernetes RBAC, so AI authority and cluster authority cannot drift apart. "Write" still means **opens a Gitea PR** — ADR-0024's guarantee that no `kubectl apply` tool exists anywhere is untouched, and is now defended a second time at the gateway. The token is preserved (`preserveToken: true`) so the MCP servers can still exchange it for a short-lived RBAC-scoped Kubernetes token and run reads as the *user*.

**5. Guardrails and budgets, permissive by default and explicitly so.** Regex prompt guards **Mask** credentials (AWS keys, `sk-` keys, JWTs, PEM private keys, DB connection strings) on the way in and on the way out, and **Audit** built-in PII detectors. Per-consumer-class request and token budgets are enforced with `conditional` local rate limits keyed on the caller's group, carrying forward the numbers already in `ai/adhar-ai`'s `values.yaml` so nobody's budget silently changes. Everything ships non-blocking for the same reason ADR-0024's Kyverno guardrail ships as `Audit`: a platform that starts by breaking requests gets switched off. Each control documents the single field to change to make it enforcing.

**6. Observability for free.** OTel spans go to the platform Tempo (`tempo.adhar-system.svc.cluster.local:4317`) carrying OTel GenAI semantic-convention attributes (`gen_ai.request.model`, `gen_ai.usage.input_tokens`, `gen_ai.usage.cost_usd`, plus `mcp.tool.name` / `mcp.tool.target`). Data-plane metrics are scraped by a `PodMonitor` and surfaced in a dedicated Grafana dashboard: request rate and latency, tokens in/out per model and provider, realized USD spend, guardrail hits, and MCP tool calls and errors. None of this is built; all of it is configured.

**7. agentgateway sits BEHIND the Cilium edge; it does not terminate TLS.** agentgateway brings its own `GatewayClass` (`agentgateway`, controller `agentgateway.dev/agentgateway`), which coexists with the platform's Cilium class `adhar` — different name, different controller, no collision, and **both are kept**. The platform keeps exactly one TLS-terminating edge: `adhar-gateway` on nodePort 30080/30443 holding the `*.<host>` wildcard. `ai.<host>` and `mcp.<host>` are ordinary platform HTTPRoutes on that edge whose backend is agentgateway's ClusterIP proxy. Reasons, in order: one certificate lifecycle instead of two; one host-port mapping in `kind.yaml.tmpl` and one LB per cloud; every package reachable the same way; and nothing is lost, because agentgateway's value is protocol awareness, which works identically one hop in. The proxy hop is plaintext inside the cluster, where Cilium is the confidentiality boundary — the same trust model every other package uses.

**8. Opt-in, exactly as wide as `adhar-ai`.** Wired into all three ApplicationSets and both environment configs with `enabled: "false"`, matching `adhar-ai` flag for flag. AI remains something an operator turns on.

## Required changes in `ai/adhar-ai`

This ADR does **not** modify `ai/adhar-ai`; that rewiring is a separate change. It requires exactly the following:

1. **Delete `manifests/llm-gateway.yaml`.** The `adhar-ai-llm-gateway` Deployment and Service are superseded. The budget environment variables it carried are now the `conditional` rate limits in `ai/agentgateway/manifests/guardrails.yaml`.
2. **Repoint `LLM_GATEWAY_URL` in all seven MCP Deployments** (`manifests/mcp-servers.yaml`) and in the agent runtime (`manifests/agent-runtime.yaml`) from `http://adhar-ai-llm-gateway.adhar-system.svc.cluster.local:8080` to `http://adhar-ai-gateway.adhar-system.svc.cluster.local:8080/v1`. Callers must then send an OpenAI-shaped body naming a model (`claude-*`, `gpt-*`, `local/*`) rather than relying on a server-side provider selection.
3. **Narrow `manifests/httproute.yaml`.** It currently claims both `ai.<host>` and `mcp.<host>` for `adhar-ai-runtime`; `ai/agentgateway` claims the same two hostnames on the same Gateway. Two HTTPRoutes claiming one hostname is resolved by creation timestamp, which is not a configuration. The runtime's route must drop both hostnames and move to a path under a hostname it owns (e.g. `adhar.<host>/ai/*`), or be dropped entirely if the Console proxies to the runtime Service directly.
4. **Add two keys to the `adhar-ai-llm` ExternalSecret template** (`manifests/llm-secret-external.yaml`): `ANTHROPIC_API_KEY` and `OPENAI_API_KEY`, so both hosted providers can be keyed at once. Today's single `API_KEY` is what the Anthropic backend reads, so the default `PROVIDER=claude` posture works unchanged; OpenAI stays unkeyed until this lands and only `gpt-*` requests are affected.
5. **Serve MCP over StreamableHTTP at `/mcp` on port 8080** in the seven `adhar-ai-mcp-*` images. `ai/agentgateway/manifests/mcp-federation.yaml` federates them at that path; if the images ship SSE instead, the fix is one `protocol: SSE` field per target in that file and no client reconfiguration.
6. **Drop per-server OIDC validation** (`OIDC_ISSUER_URL` / `OIDC_CLIENT_ID` on the MCP Deployments) once traffic is confirmed to arrive only through the gateway, and keep only the token *exchange* used for RBAC-scoped reads. Keep the Keycloak `adhar-ai` client: it remains the client users authenticate against, and its `groups` mapper is what the gateway's authorization rules read.
7. **Update `README.md` and `values.yaml`** to describe the LLM gateway as external, and to drop `images.gateway`.

## Consequences

- ✅ One place to answer "what is enforced on an AI request": identity, authorization, guardrails, budgets and tracing are declarative objects in one package, reviewable in a PR
- ✅ Prompt/response filtering, per-model cost accounting, MCP federation and OTel GenAI spans arrive as configuration instead of as a multi-quarter build of a security-critical proxy
- ✅ One endpoint, one dialect: callers name a model and never learn a provider, so hosted↔self-hosted migration (and the cost saving from routing a task to `local/*`) is a body field, not a redeployment
- ✅ External agents (Claude Code, IDEs, ChatOps) reach every governed tool through one URL with a standard OIDC token — no Adhar-specific client code
- ✅ AI spend and AI latency become first-class platform telemetry, in the platform's own Grafana and Tempo
- ✅ Apache 2.0, vendor-neutral foundation governance, no phone-home — consistent with ARCHITECTURE.md §1
- ⚠️ A new upstream dependency on the critical path of every AI request. Mitigated by: opt-in and disabled by default; pinned chart and image (`v1.5.0`, never a floating tag); no other package depends on it; and the platform runs entirely unaffected when it is off
- ⚠️ A second GatewayClass in the cluster. Deliberate and documented, but an operator now has two `GatewayClass` objects to reason about, and a Gateway created with the wrong `gatewayClassName` silently goes to the wrong controller
- ⚠️ A coordinated change with `ai/adhar-ai` (above). Until it lands the two packages claim the same two hostnames and one route reports a conflict condition
- ⚠️ Local rate limits are per proxy replica, so they multiply with replica count. Exact with the single replica this package provisions; true per-identity budgets need `rateLimit.global` and an external rate-limit service the platform does not yet run
- ⚠️ Response masking does not apply to **streamed** responses upstream, so the request-side guard is the primary control and the response guard is defence in depth for non-streaming callers
