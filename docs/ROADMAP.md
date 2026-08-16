# Adhar Roadmap

Direction, not commitment — sequencing follows community needs. Discuss on [GitHub Discussions](https://github.com/adhar-io/adhar/discussions).

The destination is fixed: the best open-source Internal Developer Platform — one command to a complete, production-grade, customizable platform on any infrastructure. The [Architecture](ARCHITECTURE.md) describes the target design (topologies T1→T3); **this roadmap is the main tracker** — every capability is listed under its phase with its current status.

**Legend**: ✅ implemented and exercised · 🟡 implemented, needs live verification/hardening · 🔜 designed, not yet built

---

## Phase 0 — Local Excellence (v0.1.x) ✅ COMPLETE

The foundation everyone evaluates first: `adhar up` on a laptop must be flawless.

- ✅ Deterministic bootstrap: Gateway API → Cilium → Gateway → ArgoCD → Gitea
- ✅ GitOps package model: full catalog wired, curated local core enabled
- ✅ Crossplane v2 control plane (23 XRDs, 34 Compositions) packaged and installable
- ✅ Automated releases (GoReleaser + GitHub Actions: binaries, GHCR images, Homebrew)
- ✅ Standard status conditions on `AdharPlatform` (ArgoCDReady, GatewayReady, GiteaReady, CrossplaneReady, GitOpsReady, aggregate Ready) with reconcile-failure messages, surfaced by `adhar get status`
- ✅ Unit test suite fully green (was ~15 known-failing tests; fixes included restoring the `adhar://` source-replication feature, source-deletion mirroring to Git, tag/branch checkout, secret patching, and scheme gaps)
- ✅ Package health dashboard: `adhar get status` shows per-package ArgoCD health/sync with a healthy/progressing/degraded summary
- ✅ E2E test covering the full bootstrap sequence (`make e2e`): `adhar up` → Ready condition → foundation components (deployments, pinned Gateway node ports, CRDs) → seeded Gitea repos via the external route → ArgoCD API auth → ApplicationSet package health → CLI status → `adhar down`

## Phase 1 — Single-Cluster Production (T2) 🟡 IMPLEMENTED

Make one managed cluster a defensible production platform. All items are code-complete with green unit/manifest tests; 🟡 marks those awaiting a live cloud run.

- ✅ **Self-managed clusters on raw cloud compute (kubeadm) — DigitalOcean live-verified**: all six providers (AWS EC2, Azure VMs, GCP GCE, DigitalOcean droplets, Civo instances, custom BYO hosts) provision plain Ubuntu machines and bootstrap Kubernetes with kubeadm over SSH — containerd, kube-proxy skipped (Cilium from the platform bootstrap replaces it, matching the Kind flow); managed services (DOKS, Civo k3s) opt-in via `useManagedK8s: true` with otherwise identical behaviour; day-2 verified live on DO: worker scale-up (join) / scale-down (drain); create→API-serving in ~5 min, Cilium to `Ready`, LetsEncrypt DNS-01 + external-dns against DigitalOcean DNS all verified against a real account (2026-08); AWS/Azure/GCP/Civo share the code path, awaiting their own live runs; single control-plane today (HA control planes with LB + stacked etcd are the next step)
- 🟡 **In-cluster controllers**: `adhar controller` runs the manager as a Deployment (leader election, health probes); installed by `adhar up --in-cluster` and by default on cloud bootstraps
- 🟡 **HA mode end-to-end**: `enableHAMode` flows config → `AdharPlatform` CR → HA manifest variants (ArgoCD/Gitea replicas + PDBs, guarded by chart-parity tests); Gitea on a bootstrap-phase CNPG cluster (`gitea-db`), Keycloak on CNPG (`keycloak-db`)
- 🟡 **Production edge**: cloud Gateway variant (LoadBalancer, wildcard listener, cert-manager-managed cert via `adhar-selfsigned` default; `adhar-letsencrypt-*` ClusterIssuers shipped), external-dns wired to Gateway HTTPRoutes (`--txt-owner-id=adhar`)
- 🟡 **SSO by default**: Keycloak OIDC wired into ArgoCD (both variants), Gitea, Grafana, Console; `credential-rotation` package rotates bootstrap credentials into Vault break-glass (enabled in the production set)
- 🟡 **Backup/DR**: Velero Schedules (daily platform / weekly cluster) with the node agent deployed and file-system backups on by default (PVC file data included, not just Kubernetes objects) + CNPG WAL archiving and daily base backups for platform databases; concrete restore runbook in [Production §5](PRODUCTION.md#5-backup-and-disaster-recovery)
- 🟡 **Upgrade story**: `adhar upgrade` — converges the foundation through the real reconcilers, diffs the local stack against the GitOps repos (`--diff-only`, `--yes`), pushes and refreshes on confirm

## Phase 2 — Multi-Cluster Platform (T3) 🟡 IMPLEMENTED

The management cluster earns its name. Code-complete; live multi-cluster validation outstanding (cloud provider field names in compositions are the first thing to verify on a real run).

- 🟡 **Control-plane / data-plane separation** ([ADR-0023](adr/0023-control-dataplane-separation.md), [plan](design/0023-control-dataplane-separation.md)): make the two cluster roles first-class and enforced — the control plane runs only fleet/platform services; **all application workloads run on data planes**. **As-built (M1–M3, awaiting a live multi-cluster run):** the `DataPlane` API (`platform.adhar.io/v1alpha1`, cluster-scoped, shortName `dp`) and its controller (`platform/controllers/dataplane/`) ship a five-phase reconcile — infra (`adopt` fully implemented; `composite`/`vcluster` real-but-tolerant) → ArgoCD registration → thin-agent profile → Cilium Mesh → observability hub → aggregate `Ready`, with `FleetStatus` rolled up on `AdharPlatform`. Placement source-of-truth is the `adhar.io/plane` label (workload appset elements carry it); a control-plane Kyverno policy (`control-plane-no-apps`, Audit) keeps application workloads off `adhar-system`. CLI: `adhar get dataplanes` and the staged, reversible `adhar migrate split-planes [--execute]` (creates the local **vcluster data plane** so apps run off the control plane even on a laptop — [ADR-0016](adr/0016-vcluster-local-first-development.md)). Still to graduate to ✅: envtest for the controller, real vcluster helm wiring, Sveltos placement in the `environments` repo, and a live T3 fleet exercise (see design §13 M4–M5)
- 🟡 **Workload clusters via GitOps**: all five cloud `CompositeCluster` compositions auto-register clusters with ArgoCD (EKS via IAM `awsAuthConfig`, GKE via `argocd-k8s-auth gcp`, AKS/DOKS/Civo via provider kubeconfig parsing)
- 🟡 **Thin workload-cluster profile**: `adhar-appset-workload.yaml` deploys the agent set (metrics-server, kyverno + policies, alloy, and now the **external-secrets** agent so data-plane workloads resolve secrets from the control-plane Vault) to every registered cluster automatically; the SPIRE agent arrives bundled with Cilium at the Cluster Mesh step below. Elements now carry the `adhar.io/plane` placement label (ADR-0023 §3)
- 🟡 **Observability hub**: Alloy ships metrics/logs/traces (cluster-labeled) to hub endpoints from the `observability-hub` ConfigMap; mimir/loki/tempo ingestion HTTPRoutes; hub packages enabled in the production set (mimir's bundled MinIO now uses a dedicated `mimir-minio-sa`, resolving the SA collision with the minio package)
- 🟡 **Cilium Cluster Mesh + SPIFFE**: mesh-ready identity (`adhar-mgmt`/ID 1) baked in; SPIRE server + agents ship in the foundation (trust domain `adhar.io`), mutual auth enforceable per-policy. The **clustermesh-apiserver** is now rendered as an opt-in manifest (`resources/cilium/clustermesh.yaml`, generated by `hack/cilium/generate-clustermesh.sh`) — kept out of the default single-node bootstrap (ADR-0012) and applied on-demand when federating a second cluster; the DataPlane controller's mesh phase drives `cilium clustermesh connect`. Cross-cluster federation itself still needs a live two-cluster run. Connect runbook in [Production §4.1](PRODUCTION.md#41-cluster-mesh-and-workload-identity-t3)
- 🟡 **Environment promotion**: Kargo pipeline (Warehouse on the environments repo; staging auto-promotes, production requires approval; promotion is a Git commit)
- 🟡 **Cluster reconstructability SLO**: monthly drill CronOperation + observer WatchOperation measuring time-to-Ready against the 1-hour SLO

## Phase 3 — Developer Experience & Ecosystem (IN PROGRESS)

From platform to product.

- 🟡 **Paved-road CI (Jenkins X on Tekton)**: `jenkins-x` package (Lighthouse webhooks/ChatOps/Tekton triggering against in-cluster Gitea, ADR-0018); enabled in the production set. The **pipeline catalog** (`jenkins-x/manifests/pipeline-catalog.yaml` — reusable `adhar-git-clone`/`adhar-test` Tasks + `adhar-pr-verify`/`adhar-release` pipelines) and **golden-path `jenkins-x.yml` templates** (in every `adhar-templates/*/skeleton`) are shipped; the release pipeline's kpack→Harbor image-build step is the remaining follow-up before a live green build
- 🟡 **Preview environments**: PR-generator ApplicationSet template ([examples/preview-environments-appset.yaml](../examples/preview-environments-appset.yaml), ADR-0017) + documented local-first workflow ([User Guide §4](USER_GUIDE.md#4-deploying-your-applications))
- 🟡 **Supply-chain policies**: Kyverno pack (signature verification, no-latest-tag, registry allowlist — Audit mode, ADR-0019 staged rollout); trivy enabled in production; **cosign own-namespace re-render is complete** — the policy-controller now installs into its own `cosign-system` namespace (`generate-manifests.sh` re-templated; CONFLICTS.md updated), resolving the `Secret/webhook-certs` collision with tekton and the `WEBHOOK_PORT` service-link hijack, so cosign is now **enabled in production** for keyless signature verification. A live signed-image enforcement run is what remains for ✅
- 🟡 **Platform auth CLI**: `adhar auth login/logout/token/whoami` against the `adhar-cli` Keycloak client (persisted sessions, auto-refresh); Keycloak groups → Kubernetes RBAC bindings shipped
- 🟡 **Full production enablement**: provider-selected ApplicationSets — Kind gets the curated core, cloud/on-prem gets the full catalog (69+ enabled; conflicts documented inline)
- 🟡 **Golden paths**: `microservice` (Go service with health/ready endpoints, graceful shutdown, distroless image, hardened manifests + HTTPRoute, ADR-0018 pipeline stub), `frontend` (nginx-unprivileged static site, same manifest set), and now a **`data-pipeline`** paved road (Backstage scaffolder template: Dagster job → Iceberg REST catalog, dbt-trino models, on-platform manifests + `jenkins-x.yml`, aligned to the ADR-0020 lakehouse direction) shipped in `application/adhar-templates` and wired into the Console catalog; Console scaffolding *execution* integration and the ML (Kubeflow/MLflow) path still 🔜
- 🟡 **Score cards**: per-service production-readiness scoring, surfaced in the Console. `application/scorecards` ships a read-only scorer CronJob that grades each service 0–100 (A–F) from real in-cluster signals (ArgoCD app health/sync, probes + resource requests, HTTPRoute exposure, backup presence, Kyverno PolicyReport pass rate, no-`:latest`), writes an `adhar-scorecards` ConfigMap, and exposes a Backstage entity annotation. Tunable weights (reliability/security/observability/operations). Enabled in the production set; a live scoring run over a running fleet is what remains for ✅
- 🟡 **Package marketplace**: community-contributed packages with a compatibility contract and provenance (signed, scanned). The contract is defined and validated: `marketplace.schema.json` (draft-07 — category, `adharCompatibility` range, dependencies, provenance, planeAffinity, stability tiers), `MARKETPLACE.md` (submission + promotion flow, ADR-0019 provenance pillars), three conforming example `adhar-package.yaml` contracts, and `hack/validate-packages.sh` (passes). Remaining for ✅: roll `adhar-package.yaml` to all ~87 packages, wire the validator into CI, and emit SBOM/attestation artifacts from the release pipeline
- 🟡 **Policy packs**: `security/policy-packs` package ships CIS- and SOC2-oriented Kyverno ClusterPolicy profiles (labeled `adhar.io/policy-pack: cis|soc2`, all Audit mode), plus the **plane-isolation** policy (`control-plane-no-apps`, ADR-0023 §6) that keeps application workloads off the control plane; wired disabled-by-default in the ApplicationSet as an opt-in
- 🟡 **Agentic platform (Adhar AI)** ([ADR-0024](adr/0024-agentic-ai-platform.md), [plan](design/0024-agentic-ai-platform.md)): a fully agentic Adhar — configure one LLM key and the platform can investigate, explain, scaffold, provision, and remediate, **through** its existing control surfaces, never around them. The **control-plane package is scaffolded** (`ai/adhar-ai/`, opt-in / disabled by default so the platform runs unaffected until keyed); the runtime/gateway/MCP container images live in the separate `adhar-io/adhar-ai` Python repo (not yet published — that's the gate to ✅):
  - 🟡 **MCP-native tools**: per-domain MCP servers (cluster, gitops, provision, observability, security, cost, catalog) manifested as distinct Deployments+Services with read tools RBAC-scoped (read-only `adhar-ai` ServiceAccount) and **write tools that only open Gitea PRs** — no direct `kubectl apply`/cloud-mutation path; exposed outward (Gateway + OIDC at `mcp.adhar.localtest.me`) so external agents (Claude Code, IDEs, ChatOps) drive Adhar through identical governed tools
  - 🟡 **GitOps-safe agent runtime**: `adhar-ai-runtime` Deployment + `adhar-ai-config` staged-autonomy ConfigMap (read-only → suggest → approve-to-apply → scoped, default `suggest`) with the event-driven operators (alert-triage, drift-explain, cost-advisor, upgrade-preflight) declared — every change lands as a reviewable, revertible Git PR
  - 🟡 **Provider-agnostic LLM gateway**: `adhar-ai-llm-gateway` Deployment reads one secret (`adhar-ai-llm`, Vault→ESO) selecting Claude (default, latest models) / OpenAI / Azure / OpenAI-compatible / Ollama (air-gapped); token/rate budget knobs central
  - 🟡 **Grounding & governance**: pgvector RAG (CNPG `adhar-ai-rag`) over docs/ADRs/runbooks + live state; Keycloak `adhar-ai` OIDC client, a Kyverno `adhar-ai-guardrails` policy on agent actions (Audit), and an `adhar.io/origin: adhar-ai` provenance label; platform runs unaffected when uninstalled or unkeyed

## Phase 4 — Data & Intelligence Platform

The lakehouse and ML story on top of the platform substrate (ADR-0020).

- 🔜 **Iceberg REST catalog service**: one platform-operated catalog (CNPG-backed) as the multi-engine interoperability point; buckets self-service via Crossplane XRs
- 🔜 **Data golden path**: Airbyte → Iceberg → dbt/Trino → Dagster scaffolded like app golden paths; lakeFS branches as "preview environments for data"
- 🔜 **Table maintenance as day-2**: compaction/snapshot-expiry/orphan-cleanup CronOperations shipped with per-table defaults
- 🔜 **ML workflow**: Kubeflow/MLflow golden path with model registry, GPU node-pool support in `CompositeCluster`, and inference serving via the Gateway
- 🔜 **Governance**: OpenMetadata wired to the catalog for lineage/discovery/ownership out of the box

## Phase 5 — Enterprise Readiness & Scale

Use cases that turn adopters into references.

- 🔜 **Multi-tenancy hardening**: vcluster-per-team self-service (`CompositeCluster` vcluster composition), hierarchical quotas, tenant-scoped observability views
- 🔜 **Hostile-tenant isolation**: Kamaji hosted control planes with dedicated node pools for regulated workloads
- 🔜 **Air-gapped installation**: image bundle + private Sigstore + offline chart pipeline as a first-class install mode
- 🔜 **Compliance evidence**: continuous CIS/SOC2 posture from Kubescape/Kyverno reports, exportable as auditor-ready artifacts
- 🔜 **Cost governance**: OpenCost budgets with alerts and showback/chargeback reports per team/namespace/cluster
- 🔜 **Managed upgrades at fleet scale**: `adhar upgrade` across registered workload clusters with canary waves and automatic rollback on health regression
- 🔜 **Identity federation**: enterprise IdP (AD/Okta/Entra) federation recipes into Keycloak with SCIM-style group sync

## Cross-Cutting Commitments

- **No breaking changes without an ADR** and a documented migration
- **Local–production parity is sacred** — nothing lands in T2/T3 that can't be exercised (scaled down) in T1
- **Every feature ships with docs** — the [docs set](README.md) is part of the definition of done
- **APIs graduate deliberately**: `v1alpha1` → `v1beta1` once controllers are in-cluster and covered by e2e
- **🟡 → ✅ requires a live run**: Phase 1/2 items graduate only after a real cloud provisioning + multi-cluster exercise; a standing verification checklist lives with each phase's PRs

## How to Influence This

- 👍 existing issues, or open one with the `roadmap` label
- Propose designs as ADRs ([docs/adr](adr/README.md))
- Bring a use case to [Slack](https://join.slack.com/t/adharworkspace/shared_invite/zt-26586j9sx-QGrIejNigvzGJrnyH~IXww) — real adoption stories set priorities
