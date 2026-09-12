# Adhar Platform Documentation

Adhar is an open Internal Developer Platform: one command provisions a complete, production-grade, customizable platform of **91 open-source packages**, on six clouds or your laptop. This is the index — find the right page in one screen.

**In one paragraph**: `adhar up` bootstraps a strictly ordered foundation — Cilium (CNI + Gateway API) → ArgoCD → Gitea — then seeds the in-cluster Git repos and hands control to GitOps. A single ApplicationSet deploys every enabled package from Git, a Crossplane v2 control plane provides namespaced self-service infrastructure APIs, and from that moment every platform change is a reviewable Git commit. The same architecture runs on a laptop (Kind), on one production cluster, or on a control plane governing a fleet of data planes.

---

## Get started

| Document | Read it when |
|---|---|
| [Getting Started](GETTING_STARTED.md) | First time: install, `adhar up`, tour the services, make a first GitOps change, tear down |
| [User Guide](USER_GUIDE.md) | Daily work: the mental model, CLI reference, deploying apps, requesting infrastructure, plus the day-0/day-1 bootstrap manual (ports, URLs, flags, verify-healthy checklist, safe re-run, upgrade, teardown) |
| [Customization Guide](CUSTOMIZATION.md) | You want to change something — all nine extension points, from flipping a package on to adding a cloud provider |
| [examples/](../examples/) | You want a working YAML for a CRD or a Crossplane API to copy |

## Operate

| Document | Read it when |
|---|---|
| [Production Guide](PRODUCTION.md) | Running it for real: topology choice, HA sizing, hardening checklist, edge (DNS/TLS/LB), backup & DR runbooks, upgrades |
| [Production Access](PRODUCTION_ACCESS.md) | You provisioned a cloud platform and need to reach it — URLs, credentials, kubeconfig, DNS |
| [DigitalOcean, end to end](DIGITALOCEAN_PRODUCTION.md) | You are on DigitalOcean: every field, command, resource and limit, as executed on a live cluster |
| [Provider Guide](PROVIDER_GUIDE.md) | You are targeting Kind, AWS, Azure, GCP, DigitalOcean, Civo or your own cluster — or adding a provider |
| [Troubleshooting](TROUBLESHOOTING.md) | You have an error on screen: per-phase failure signatures, confirmation commands, root causes, fixes |
| [Release Guide](RELEASE_GUIDE.md) | You are cutting a release: versioning and the GoReleaser/GitHub Actions pipeline |

## Understand

| Document | Read it when |
|---|---|
| [Architecture](ARCHITECTURE.md) | The definitive design: goals, principles, four layers, bootstrap/GitOps lifecycle, package catalogue and the namespace rule, networking, security, topologies and what is verified, extensibility, the AI layer, observability |
| [Control Plane In Depth](CONTROL_PLANE.md) | The Crossplane v2 control plane from first principles: 25 XRDs, 47 Compositions, install order, a request's life, debugging, extension, known gaps |
| [Package conflicts](../platform/stack/packages/CONFLICTS.md) | You are adding or enabling a package — the collision register for the shared `adhar-system` namespace, with a scan script |
| [Control-plane conventions](../platform/controlplane/CONVENTIONS.md) | You are writing an XRD or a Composition — the terse rule book |
| [Roadmap](ROADMAP.md) | You want phase status: local excellence → single-cluster production → multi-cluster → developer experience → data & intelligence → enterprise scale |

## Decide

Decisions with lasting consequences are [ADRs](adr/README.md); each has a matching low-level [design doc](design/README.md) — the ADR says *what and why*, the design doc says *how it is actually built*. ADRs are immutable once accepted; a change of direction gets a new ADR.

| # | Decision | Design doc |
|---|---|---|
| [0001](adr/0001-management-cluster-first.md) | Management-cluster-first with a two-phase bootstrap | [design](design/0001-management-cluster-first.md) |
| [0002](adr/0002-cilium-cni-and-gateway.md) | Cilium as CNI, kube-proxy replacement and Gateway API implementation | [design](design/0002-cilium-cni-and-gateway.md) |
| [0003](adr/0003-in-cluster-gitea.md) | Self-hosted in-cluster Gitea as the platform's source of truth | [design](design/0003-in-cluster-gitea.md) |
| [0004](adr/0004-applicationset-package-model.md) | One ApplicationSet with an enabled-gated package list | [design](design/0004-applicationset-package-model.md) |
| [0005](adr/0005-crossplane-v2-namespaced.md) | Crossplane v2 namespaced XRs for self-service infrastructure | [design](design/0005-crossplane-v2-namespaced.md) |
| [0006](adr/0006-embedded-bootstrap-manifests.md) | Embedded, pre-rendered manifests for bootstrap — no network dependency | [design](design/0006-embedded-bootstrap-manifests.md) |
| [0007](adr/0007-dual-provisioning-paths.md) | Two provisioning paths: imperative provider interface + declarative Crossplane | [design](design/0007-dual-provisioning-paths.md) |
| [0008](adr/0008-keycloak-platform-identity.md) | Keycloak as the platform identity provider, OIDC everywhere | [design](design/0008-keycloak-platform-identity.md) |
| [0009](adr/0009-secrets-eso-vault.md) | Secrets: ESO as the sync plane, a Vault-compatible backend as source of truth, never Git | [design](design/0009-secrets-eso-vault.md) |
| [0010](adr/0010-observability-lgtm-otel.md) | Observability: OTel collection, Grafana LGTM storage, hub-and-spoke | [design](design/0010-observability-lgtm-otel.md) |
| [0011](adr/0011-shared-platform-namespace.md) | One shared namespace (`adhar-system`) for platform packages — one exception, `buildpack` | [design](design/0011-shared-platform-namespace.md) |
| [0012](adr/0012-single-node-resilience-tuning.md) | Resilience tuning for single-node local clusters | [design](design/0012-single-node-resilience-tuning.md) |
| [0013](adr/0013-sso-bootstrap-config-job.md) | SSO bootstrap via an idempotent in-cluster config job | [design](design/0013-sso-bootstrap-config-job.md) |
| [0014](adr/0014-package-lifecycle-operations.md) | Package lifecycle: toggling, verification, clean removal | [design](design/0014-package-lifecycle-operations.md) |
| [0015](adr/0015-idp-critical-pillars.md) | The critical pillars of the IDP — the tests every addition must pass | [design](design/0015-idp-critical-pillars.md) |
| [0016](adr/0016-vcluster-local-first-development.md) | vcluster as the virtual-cluster primitive for local-first development and tenancy | [design](design/0016-vcluster-local-first-development.md) |
| [0017](adr/0017-preview-environments.md) | Ephemeral preview environments per pull request | [design](design/0017-preview-environments.md) |
| [0018](adr/0018-jenkins-x-ci-model.md) | CI on the platform, promotion via GitOps — *superseded: Tekton `app-ci` is the paved road* | [design](design/0018-jenkins-x-ci-model.md) |
| [0019](adr/0019-secure-supply-chain-chainguard.md) | Secure supply chain: Chainguard images, Sigstore signing, policy admission | [design](design/0019-secure-supply-chain-chainguard.md) |
| [0020](adr/0020-iceberg-data-lakehouse.md) | Data lakehouse on Apache Iceberg over platform object storage | [design](design/0020-iceberg-data-lakehouse.md) |
| [0021](adr/0021-day2-operations-first-class.md) | Day-2 operations as a first-class product surface | [design](design/0021-day2-operations-first-class.md) |
| [0022](adr/0022-custom-clusters-no-managed-k8s.md) | Custom clusters on raw cloud infrastructure — managed Kubernetes is opt-in | [design](design/0022-custom-clusters-no-managed-k8s.md) |
| [0023](adr/0023-control-dataplane-separation.md) | Control-plane / data-plane separation with a first-class `DataPlane` API | [design](design/0023-control-dataplane-separation.md) |
| [0024](adr/0024-agentic-ai-platform.md) | Agentic AI platform — MCP-native tools and a GitOps-safe agent runtime | [design](design/0024-agentic-ai-platform.md) |
| [0025](adr/0025-ai-gateway-agentgateway.md) | agentgateway as the platform's AI data plane (LLM routing, federated MCP, guardrails) | — |

One design document has no ADR: [CLI command landscape](design/cli-command-landscape.md) — the developer- and operator-facing command surface, modelled on CloudFoundry.

## Project

| Document | Read it when |
|---|---|
| [Contributing](../CONTRIBUTING.md) | You want to open a PR (DCO required) |
| [Security Policy](../SECURITY.md) | You have a vulnerability to report, or need supported versions |
| [Changelog](../CHANGELOG.md) | You want the version history |

## Getting help

| Channel | Use for |
|---------|---------|
| [Slack](https://join.slack.com/t/adharworkspace/shared_invite/zt-26586j9sx-QGrIejNigvzGJrnyH~IXww) | Questions, real-time help |
| [GitHub Issues](https://github.com/adhar-io/adhar/issues) | Bugs, feature requests |
| [GitHub Discussions](https://github.com/adhar-io/adhar/discussions) | Ideas, design conversations |
