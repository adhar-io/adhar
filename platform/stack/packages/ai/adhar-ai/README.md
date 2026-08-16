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
   `observability`, `security`, `cost`, `catalog`). **Read** tools are RBAC-scoped
   (get/list/watch via the `adhar-ai` ServiceAccount). **Write** tools open **Gitea PRs only** —
   there is no `kubectl apply` / `argo sync` / cloud-mutation tool anywhere. The same servers are
   exposed **outward** through the Gateway + OIDC, so external agents (Claude Code, IDEs, ChatOps)
   drive Adhar through the identical governed tools.
2. **GitOps-safe agent runtime** — an assistant (`adhar ai` CLI + Console chat) and event-driven
   operators (`alert-triage`, `drift-explain`, `cost-advisor`, `upgrade-preflight`) on a
   **staged-autonomy** ladder (`read-only → suggest → approve-to-apply → scoped`, default
   `suggest`). Every change lands as a reviewable, revertible Git PR that ArgoCD reconciles after a
   human merges.
3. **Provider-agnostic LLM gateway** — one secret (`adhar-ai-llm`, Vault→ESO) selects Claude
   (default, latest models) / OpenAI / Azure OpenAI / any OpenAI-compatible endpoint / Ollama
   (air-gapped). Token/rate budgets are enforced centrally; the raw key never leaves the gateway.
4. **Grounding & governance** — pgvector RAG (on CNPG) over docs/ADRs/runbooks + live platform
   state, so suggestions match Adhar's real conventions. Keycloak identity on every action, a
   Kyverno guardrail on agent-originated resources (Audit), and full audit of every tool call to
   the observability hub.

## Manifests

| File | What |
|---|---|
| `manifests/namespace-and-rbac.yaml` | `adhar-ai` SA, **read-only** ClusterRole (get/list/watch), namespaced self Role (no cluster-mutating verbs) |
| `manifests/llm-gateway.yaml` | `adhar-ai-llm-gateway` Deployment+Service; provider-agnostic; `envFrom` the `adhar-ai-llm` secret; budget knobs |
| `manifests/llm-secret-external.yaml` | ExternalSecrets `adhar-ai-llm` (provider=claude default, latest Claude model) + `adhar-ai-bot` (Gitea PR identity) |
| `manifests/mcp-servers.yaml` | Seven per-domain MCP Deployments+Services; read RBAC-scoped, write = PR-only |
| `manifests/agent-runtime.yaml` | `adhar-ai-runtime` Deployment + `adhar-ai-config` ConfigMap (staged autonomy, default `suggest`) |
| `manifests/httproute.yaml` | HTTPRoute for `ai.adhar.localtest.me` + `mcp.adhar.localtest.me` on `adhar-gateway` |
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
- **Identity + audit.** Keycloak gates access; reads run under the caller's RBAC via token
  exchange; every tool call is audited to Loki.

## Degrades gracefully

The platform runs **fully unaffected** when this package is uninstalled *or* installed but unkeyed:
with no `adhar-ai/llm` entry in Vault the gateway reports "unkeyed", stays read-only, and no other
package depends on it. Disabled by default for local (`enabled: "false"`); enable in the
production/GitOps set.

## Images (do not yet exist — to be published by `adhar-io/adhar-ai`)

- `ghcr.io/adhar-io/adhar-ai-gateway:latest`
- `ghcr.io/adhar-io/adhar-ai-runtime:latest`
- `ghcr.io/adhar-io/adhar-ai-mcp-{cluster,gitops,provision,observability,security,cost,catalog}:latest`
