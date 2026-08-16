# ADR-0024: Agentic AI platform — MCP-native tools and a GitOps-safe agent runtime

**Status**: Accepted (control-plane package `ai/adhar-ai` scaffolded — MCP servers, LLM gateway, agent runtime, pgvector RAG, OIDC, guardrails — opt-in/disabled by default; runtime container images and live enablement are Roadmap Phase 3) · **Date**: 2026-08

## Context

Operating an IDP demands expert fluency across many surfaces — Kubernetes, ArgoCD, Gitea, Crossplane, Keycloak, Vault, the LGTM stack, Kyverno, OpenCost. The [roadmap](../ROADMAP.md) already lists "AI-assisted operations: platform-aware assistant for debugging and change suggestions" as a Phase 3 aspiration (🔜). The goal now is bigger: a **fully agentic** Adhar, where an operator or developer configures a single LLM key and the platform gains the ability to investigate, explain, scaffold, provision, and remediate — grounded in the platform's real state and constrained by its existing control surfaces.

The hard questions are *safety* and *placement*, not model access:

- An agent with `kubectl apply` and a cluster-admin token is a catastrophe waiting for a prompt injection. Adhar's whole thesis is **GitOps-first, policy-enforced, RBAC-scoped, reviewable change** — an AI layer that bypasses that thesis would be a regression, not a feature.
- The agent/MCP/LLM ecosystem is overwhelmingly **Python**; Adhar's core is **Go 1.26**. Cramming an agent runtime into the Go binary would fight the ecosystem and bloat the core.
- Where should intelligence live relative to [ADR-0023](0023-control-dataplane-separation.md)'s control/data-plane split?

Alternatives considered:

- **Chatbot bolted onto the Console, calling the Kubernetes API directly** — fast demo, but it either is read-only (limited value) or holds broad write credentials (unsafe) and sits outside GitOps, so its changes bypass review, policy, and drift-detection.
- **A hosted/SaaS AI copilot** — conflicts with the 100%-open-source, no-phone-home, you-own-it principle ([ARCHITECTURE.md §1](../ARCHITECTURE.md)) and leaks platform state to a third party.
- **An MCP-native agent layer that acts *through* the platform's existing control surfaces** — reads via RBAC-scoped tools, writes only by opening Git commits/PRs, governed by the same policy and audit as humans.

## Decision

Build **Adhar AI**: an agentic control layer that exposes every Adhar capability as **MCP (Model Context Protocol) tools** and drives them with an LLM through an agent runtime — **GitOps-native and least-privilege by construction. The agent proposes changes as Git commits/PRs and performs only RBAC-scoped reads and narrowly-scoped actions; it never holds a broad write path to a cluster.** The agent is a very capable *contributor and investigator*, not a root shell.

**1. A separate Python repository, shipped as a platform package.** Adhar AI lives in `adhar-io/adhar-ai` (Python — the native ecosystem for the `mcp` SDK, agent frameworks, and LLM SDKs). It builds container images to GHCR and installs into the platform as a standard Adhar **package** (`platform/stack/packages/ai/adhar-ai/`), delivered by the ApplicationSet ([ADR-0004](0004-applicationset-package-model.md)) and wired to SSO ([ADR-0008](0008-keycloak-platform-identity.md)), secrets ([ADR-0009](0009-secrets-eso-vault.md)), and the Gateway like any other service. Keeping it a separate repo keeps the Go core lean (respecting the ADR-0006 bootstrap boundary) while integrating through the platform's normal contracts. It runs on the **control plane** ([ADR-0023](0023-control-dataplane-separation.md)) — it orchestrates the fleet; data planes need no AI components.

**2. MCP tool servers, one per domain.** Each wraps an Adhar capability as typed MCP tools, tagged **read** or **write**:

| Server | Read tools | Write tools (Git-PR only) |
|---|---|---|
| `mcp-cluster` | pods/events/logs, describe, health across the fleet | — |
| `mcp-gitops` | ArgoCD app status/sync/diff | open Gitea PRs against `packages`/`environments` |
| `mcp-provision` | Crossplane XR/claim state | author `CompositeCluster`/`Database`/… XRs as PRs |
| `mcp-observability` | PromQL/LogQL/TraceQL, SLO burn-rate, correlation | — |
| `mcp-security` | Kyverno/Trivy/Kubescape findings, policy explain | draft policy exceptions as PRs |
| `mcp-cost` | OpenCost queries, showback | — |
| `mcp-catalog` | package catalog, golden-path templates | scaffold a service/data path as a PR |

Every mutation is a Git change against Gitea, which then flows through normal review → CI → ArgoCD. There is no tool that calls `kubectl apply`, `argocd app set`, or a cloud API directly with mutating scope.

**3. A provider-agnostic LLM gateway with one key to configure.** A single secret (`adhar-ai-llm`, sourced from Vault via ESO) carries the provider and API key. The gateway supports Anthropic Claude (default; latest models), OpenAI, Azure OpenAI, any OpenAI-compatible endpoint, and local models via Ollama for air-gapped installs. **Configure the key → capabilities turn on**; no code changes, no rebuild.

**4. An agent runtime with human-in-the-loop writes.** A planning agent loop uses the MCP tools to investigate and compose answers or **proposed** changes. Modes: an interactive **assistant** (Console + `adhar ai` CLI), and event-driven **operators** (on an Alertmanager alert → triage → open a remediation PR; on drift → explain; on a cost breach → suggest). Any write pauses for approval — surfaced as a Git PR a human merges, so the agent's authority is exactly a contributor's.

**5. Grounding in real state.** The agent is fed live platform context (ArgoCD, Prometheus, the package catalog, the ADRs) and a **pgvector** store (on CNPG) over the docs, ADRs, runbooks, and past incidents — so its suggestions follow Adhar's actual conventions and current state, not a generic prior.

**6. Identity, policy, and audit.** Users authenticate through Keycloak; the agent acts under the requester's RBAC (for reads) and opens PRs as an auditable bot identity (for writes). A dedicated Kyverno/OPA policy set governs agent actions, and every tool call (who, what, why, which model, what it changed) is logged to the observability hub.

**7. MCP-native, outward too.** The same MCP servers are exposed (Gateway + OIDC) to **external** agent clients — Claude Code, IDE assistants, ChatOps — so a developer's own agent can operate Adhar through the identical, governed tools. Adhar becomes an MCP-native platform, not just one with a built-in bot.

## Consequences

- ✅ **One key, platform-wide agency, safely** — configure an LLM key and the platform can investigate, scaffold, provision, and remediate, with every change flowing through GitOps review, policy, RBAC, and audit.
- ✅ **Grounded and current** — RAG over ADRs/docs/runbooks plus live state means suggestions match Adhar's conventions and reality, reducing hallucinated advice.
- ✅ **MCP-native both ways** — built-in agents *and* external agents (Claude Code, IDEs) drive the platform through the same governed tools; the tool layer is the durable asset, models are swappable.
- ✅ **Right ecosystem, clean core** — Python where the agent/MCP ecosystem lives; the Go core stays lean; integration is via the standard package/SSO/secrets/Gateway contracts, and it composes onto the ADR-0023 control plane.
- ✅ **Open and self-hosted** — no SaaS copilot, no state leaving the cluster; local/OpenAI-compatible/Ollama backends keep air-gapped installs viable.
- ⚠️ **LLM cost, latency, and nondeterminism** — mitigated by read-only defaults, token/rate budgets, caching, and human approval for writes; agency is opt-in and scoped.
- ⚠️ **New security surface** (prompt injection, over-broad tools, data exfiltration) — mitigated by least-privilege per-tool scopes, **PR-only writes** (no direct mutation path), policy admission on agent actions, and full audit; the agent can never exceed a human contributor's authority.
- ⚠️ **A second repository to maintain and version-sync** — the contract is the MCP tool schemas plus package-version pinning between `adhar` and `adhar-ai`; the platform must degrade gracefully (feature simply off) when Adhar AI is not installed or no key is configured.
- ⚠️ **Trust calibration** — teams must learn what to let the agent do autonomously vs. propose; the staged rollout (read-only → suggest → approve-to-apply → scoped autonomy) is deliberate.

See the full detailed design (Python MCP servers, tool schemas, the PR write path, LLM gateway, agent loop, RBAC/RAG, package manifests, CLI contract, threat model, milestones, tests) in [docs/design/0024-agentic-ai-platform.md](../design/0024-agentic-ai-platform.md).
