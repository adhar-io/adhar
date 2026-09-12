# Adhar AI — agentic control layer (package)

Implements [ADR-0024](../../../../docs/adr/0024-agentic-ai-platform.md) ([design](../../../../docs/design/0024-agentic-ai-platform.md)).
A **control-plane** package (ADR-0023) that gives Adhar an agentic layer: configure **one LLM key**
and the platform can investigate, explain, scaffold, provision, and remediate — **through** its
existing control surfaces (GitOps, RBAC, policy, audit), never around them. The agent is a very
capable *contributor and investigator*, not a root shell.

The runtime lives in the separate `adhar-io/adhar-ai` (Python) repo and ships here as container
images; this package is the standard manifest set that installs it via the ApplicationSet.

## The four capabilities

1. **MCP-native tools** — seven per-domain MCP servers (`cluster`, `gitops`, `provision`,
   `observability`, `security`, `cost`, `catalog`), each serving StreamableHTTP at `/mcp` on 8080.
   **Read** tools are RBAC-scoped (get/list/watch via the `adhar-ai` ServiceAccount). **Write**
   tools open **Gitea PRs only** — there is no `kubectl apply` / `argo sync` / cloud-mutation tool
   anywhere. They are exposed **outward** as ONE federated endpoint, `https://mcp.<host>/mcp` on
   [`ai/agentgateway`](../agentgateway/) ([ADR-0025](../../../../docs/adr/0025-ai-gateway-agentgateway.md)),
   which multiplexes all seven, validates the Keycloak JWT once and authorizes per tool — so
   external agents (Claude Code, IDEs, ChatOps) drive Adhar through the identical governed tools
   with one URL and one token.
2. **GitOps-safe agent runtime** — an assistant (`adhar ai` CLI + Console chat) and event-driven
   operators (`alert-triage`, `drift-explain`, `cost-advisor`, `upgrade-preflight`) on a
   **staged-autonomy** ladder (`read-only → suggest → approve-to-apply → scoped`, default
   `suggest`). Every change lands as a reviewable, revertible Git PR that ArgoCD reconciles after a
   human merges.
3. **LLM access through the platform AI data plane (external to this package)** — completions go
   to [`ai/agentgateway`](../agentgateway/) at `http://adhar-ai-gateway.adhar-system.svc.cluster.local:8080/v1`
   (publicly `https://ai.<host>/v1/chat/completions`). Callers send an OpenAI-shaped body and pick
   a provider **by model name**: `claude-*` → Anthropic, `gpt-*`/`o[1-9]-*` → OpenAI, `local/*` →
   the in-cluster [`ai/vllm`](../vllm/). One secret (`adhar-ai-llm`, Vault→ESO) still holds the
   keys — `API_KEY`/`ANTHROPIC_API_KEY` and `OPENAI_API_KEY` — read server-side by the gateway and
   never returned to a caller. Token/rate budgets and prompt guardrails are enforced there too.
   **This package no longer ships an LLM gateway** ([ADR-0025](../../../../docs/adr/0025-ai-gateway-agentgateway.md)).
4. **Grounding & governance** — pgvector RAG (on CNPG) over docs/ADRs/runbooks + live platform
   state, so suggestions match Adhar's real conventions. Keycloak identity on every action, a
   Kyverno guardrail on agent-originated resources (Audit), and full audit of every tool call to
   the observability hub.

## Manifests

| File | What |
|---|---|
| `manifests/namespace-and-rbac.yaml` | `adhar-ai` SA, **read-only** ClusterRole (get/list/watch), namespaced self Role (no cluster-mutating verbs) |
| `manifests/llm-secret-external.yaml` | ExternalSecrets `adhar-ai-llm` (`API_KEY`/`ANTHROPIC_API_KEY` + `OPENAI_API_KEY`, provider=claude default) + `adhar-ai-bot` (Gitea PR identity) |
| `manifests/mcp-servers.yaml` | Seven per-domain MCP Deployments+Services (StreamableHTTP `/mcp`:8080); read RBAC-scoped, write = PR-only; no per-server OIDC — the gateway validates |
| `manifests/agent-runtime.yaml` | `adhar-ai-runtime` Deployment + `adhar-ai-config` ConfigMap (staged autonomy, default `suggest`) |
| `manifests/httproute.yaml` | HTTPRoute for `agent.adhar.localtest.me` (the runtime) on `adhar-gateway`. `ai.<host>` and `mcp.<host>` belong to `ai/agentgateway` |
| `manifests/oidc-client.yaml` | Keycloak `adhar-ai` client payload ConfigMap + registration Job (identity on agent actions) |
| `manifests/kyverno-policy.yaml` | `adhar-ai-guardrails` ClusterPolicy (**Audit**): require origin label, block direct mutation |
| `manifests/pgvector-rag.yaml` | CNPG `adhar-ai-rag` Cluster (pgvector, `ServerSideApply=true`) for RAG grounding |

## Safety model (why this is not a cluster-admin bot)

- **No write RBAC.** The `adhar-ai` ClusterRole is get/list/watch only. The agent physically cannot
  mutate cluster state with its identity.
- **PR-only writes.** Every mutation is a Gitea PR authored by the commit-only `adhar-ai-bot`,
  flowing through normal review → CI → ArgoCD. The agent's authority is exactly a contributor's.
- **Policy backstop.** `adhar-ai-guardrails` (Audit) attributes and flags any AI-originated
  resource that skips the PR path.
- **Identity + audit.** Keycloak gates access — once, in the AI data plane, for every LLM call and
  every federated tool call (the `adhar-ai` client and its `groups` mapper are what
  `ai/agentgateway`'s CEL authorization reads; read tools need `platform-developer`, PR-opening
  tools need `platform-admin`). The bearer token is preserved, so reads still run under the
  caller's RBAC via token exchange; every tool call is audited to Loki.

## Degrades gracefully

The platform runs **fully unaffected** when this package is uninstalled *or* installed but unkeyed:
with no `adhar-ai/llm` entry in Vault the AI data plane has no credential, the agent stays
read-only, and no other package depends on it. Disabled by default everywhere (`enabled: "false"`);
enable it together with `ai/agentgateway`, which is what serves its LLM and MCP endpoints.

## Images (do not yet exist — to be published by `adhar-io/adhar-ai`)

- `ghcr.io/adhar-io/adhar-ai-runtime:latest`
- `ghcr.io/adhar-io/adhar-ai-mcp-{cluster,gitops,provision,observability,security,cost,catalog}:latest`
