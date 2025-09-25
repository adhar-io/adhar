# Command Coverage Roadmap

This roadmap tracks the progress of implementing Crossplane-backed support for each `adhar` CLI command. Every command should eventually be backed by one or more composite resources so that the CLI becomes a thin client over the control plane API.

| Order | Command | Composite Resource | Status | Next Work |
| ----- | ------- | ------------------ | ------ | --------- |
| 1 | `apps` | `CompositeApplication` | In Progress | Finish claim CRUD (list/deploy), expand ArgoCD composition to include destination selectors and environment targeting |
| 2 | `auth` | `CompositeAuthStack` | Pending | Design XRD for identity providers, create base composition for Keycloak/OIDC and surface CLI provisioning |
| 3 | `backup` | `CompositeBackupPolicy` | Pending | Model backup schedules and retention policies, integrate with Velero compositions |
| 4 | `cluster` | `CompositeCluster` | In Progress | Add multi node pool orchestration and provider-specific guardrails/tests |
| 5 | `config` | `CompositePlatformConfig` | Pending | Define global platform policy schema, link to existing `config` CLI management |
| 6 | `db` | `CompositeDatabase` | Pending | Provide managed database abstraction with provider-specific compositions (RDS, CloudSQL, AzureDB) |
| 7 | `down` | `CompositeTeardown` | Pending | Implement teardown workflows referencing existing platform resources |
| 8 | `env` | `CompositeEnvironment` | Pending | Structure environment definitions and secrets injection, map to CLI env workflows |
| 9 | `get` | `CompositeQuery` | Pending | Craft read-only aggregation composites for inventory-style CLI commands |
| 10 | `gitops` | `CompositeGitOps` | In Progress | Add namespace/aggregate views, expand ApplicationSet handling and CLI sync hooks |
| 11 | `health` | `CompositeHealthCheck` | Pending | Define health check bundles and integrate with observability stack |
| 12 | `logs` | `CompositeLogging` | Pending | Model log pipelines and sinks per environment |
| 13 | `metrics` | `CompositeMetrics` | Pending | Capture metrics pipeline resources (Prometheus/Thanos) |
| 14 | `network` | `CompositeNetwork` | Pending | Define VPC, subnet, mesh abstractions with provider compositions |
| 15 | `pipeline` | `CompositePipeline` | Pending | Introduce CI/CD pipeline composites tied to Tekton/Argo Workflows |
| 16 | `policy` | `CompositePolicyBundle` | Pending | Capture policy packs and OPA Gatekeeper integration |
| 17 | `restore` | `CompositeRestore` | Pending | Complement backup by modeling restore workflows |
| 18 | `scale` | `CompositeScalingPolicy` | Pending | Provide scaling policies for apps and infrastructure |
| 19 | `secrets` | `CompositeSecretStore` | Pending | Manage secret store instances and rotation policies |
| 20 | `security` | `CompositeSecurityStack` | Pending | Cover security scanning, SAST/DAST compositions |
| 21 | `service` | `CompositeService` | Pending | Define service catalog entries and dependency mappings |
| 22 | `storage` | `CompositeStorage` | Pending | Introduce object/block storage composites per provider |
| 23 | `traces` | `CompositeTracing` | Pending | Model tracing pipeline resources |
| 24 | `up` | `CompositeBootstrap` | Pending | Declarative bootstrap orchestrations combining core stacks |
| 25 | `version` | `CompositePlatformVersion` | Pending | Control plane version pinning and upgrade orchestration |
| 26 | `webhook` | `CompositeWebhook` | Pending | External event hook integrations and routing |

> **Legend**
> - **Pending** – No XRD/composition yet.
> - **In Progress** – XRD stubbed out, composition partially complete.
> - **Complete** – Command fully backed by tested compositions.

We will execute features sequentially following the order column, promoting each command to **Complete** once its XRD, compositions, CLI integration, and validation tests are in place.
