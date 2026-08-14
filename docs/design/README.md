# Detailed Design Documents

One detailed design document per [ADR](../adr/README.md), numbered to match. ADRs capture the *decision* (context → decision → consequences); these documents capture the *low-level design* — for accepted/built decisions, the **as-built** implementation (real types, files, functions, manifests, flows, tests); for forward-looking decisions, the design to build (components, APIs, manifests, milestones, risks).

Each doc verifies its cited paths/symbols against the tree and closes with a **drift** section flagging where the code diverges from its ADR.

| # | Design doc | ADR |
|---|-----------|-----|
| 0001 | [Management-cluster-first & two-phase bootstrap](0001-management-cluster-first.md) | [ADR-0001](../adr/0001-management-cluster-first.md) |
| 0002 | [Cilium CNI, kube-proxy replacement, Gateway API](0002-cilium-cni-and-gateway.md) | [ADR-0002](../adr/0002-cilium-cni-and-gateway.md) |
| 0003 | [In-cluster Gitea](0003-in-cluster-gitea.md) | [ADR-0003](../adr/0003-in-cluster-gitea.md) |
| 0004 | [ApplicationSet package model](0004-applicationset-package-model.md) | [ADR-0004](../adr/0004-applicationset-package-model.md) |
| 0005 | [Crossplane v2 namespaced XRs](0005-crossplane-v2-namespaced.md) | [ADR-0005](../adr/0005-crossplane-v2-namespaced.md) |
| 0006 | [Embedded bootstrap manifests](0006-embedded-bootstrap-manifests.md) | [ADR-0006](../adr/0006-embedded-bootstrap-manifests.md) |
| 0007 | [Dual provisioning paths](0007-dual-provisioning-paths.md) | [ADR-0007](../adr/0007-dual-provisioning-paths.md) |
| 0008 | [Keycloak platform identity](0008-keycloak-platform-identity.md) | [ADR-0008](../adr/0008-keycloak-platform-identity.md) |
| 0009 | [Secrets: ESO + Vault](0009-secrets-eso-vault.md) | [ADR-0009](../adr/0009-secrets-eso-vault.md) |
| 0010 | [Observability: LGTM + OTel](0010-observability-lgtm-otel.md) | [ADR-0010](../adr/0010-observability-lgtm-otel.md) |
| 0011 | [Shared platform namespace](0011-shared-platform-namespace.md) | [ADR-0011](../adr/0011-shared-platform-namespace.md) |
| 0012 | [Single-node resilience tuning](0012-single-node-resilience-tuning.md) | [ADR-0012](../adr/0012-single-node-resilience-tuning.md) |
| 0013 | [SSO bootstrap config job](0013-sso-bootstrap-config-job.md) | [ADR-0013](../adr/0013-sso-bootstrap-config-job.md) |
| 0014 | [Package lifecycle operations](0014-package-lifecycle-operations.md) | [ADR-0014](../adr/0014-package-lifecycle-operations.md) |
| 0015 | [IDP critical pillars & their tests](0015-idp-critical-pillars.md) | [ADR-0015](../adr/0015-idp-critical-pillars.md) |
| 0016 | [vCluster local-first development](0016-vcluster-local-first-development.md) | [ADR-0016](../adr/0016-vcluster-local-first-development.md) |
| 0017 | [Preview environments](0017-preview-environments.md) | [ADR-0017](../adr/0017-preview-environments.md) |
| 0018 | [Jenkins X CI model on Tekton](0018-jenkins-x-ci-model.md) | [ADR-0018](../adr/0018-jenkins-x-ci-model.md) |
| 0019 | [Secure supply chain (Chainguard/Sigstore)](0019-secure-supply-chain-chainguard.md) | [ADR-0019](../adr/0019-secure-supply-chain-chainguard.md) |
| 0020 | [Iceberg data lakehouse](0020-iceberg-data-lakehouse.md) | [ADR-0020](../adr/0020-iceberg-data-lakehouse.md) |
| 0021 | [Day-2 operations, first-class](0021-day2-operations-first-class.md) | [ADR-0021](../adr/0021-day2-operations-first-class.md) |
| 0022 | [Custom clusters, no managed K8s](0022-custom-clusters-no-managed-k8s.md) | [ADR-0022](../adr/0022-custom-clusters-no-managed-k8s.md) |
| 0023 | [Control-plane / data-plane separation](0023-control-dataplane-separation.md) 🔜 | [ADR-0023](../adr/0023-control-dataplane-separation.md) |
| 0024 | [Agentic AI platform (Adhar AI)](0024-agentic-ai-platform.md) 🔜 | [ADR-0024](../adr/0024-agentic-ai-platform.md) |

🔜 = forward-looking design (proposed, not yet built). All others are as-built.
