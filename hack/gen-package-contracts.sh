#!/usr/bin/env bash
#
# gen-package-contracts.sh — scaffold the marketplace contract
# (`adhar-package.yaml`) for every platform package that does not have one yet.
#
# A package directory is `platform/stack/packages/<category>/<name>/` that holds
# at least one of: `manifests/`, `values.yaml`, `generate-manifests.sh`.
# Placeholder directories (only `.gitkeep` / `SKIPPED.md`) are not packages.
#
# IDEMPOTENT: an existing `adhar-package.yaml` is NEVER overwritten — the script
# only creates the missing ones. Re-running it after adding a package scaffolds
# just that package. Hand-edit the generated file afterwards; the generator will
# not fight you.
#
# What is derived from the tree (no hand-maintained duplication):
#   * name / category ......... the directory path (the validator re-checks this)
#   * version / appVersion .... CHART_VERSION / APP_VERSION / OPERATOR_VERSION /
#                               KPACK_VERSION / PIPELINE_VERSION / KFP_VERSION /
#                               VERSION in generate-manifests.sh
#   * provenance.upstreamChart  the `helm repo add` alias + chart in the
#                               `helm template` line (or an oci:// reference)
#   * dependencies ............ manifests that reference a platform capability:
#                               postgresql.cnpg.io -> cnpg, ExternalSecret ->
#                               external-secrets, oauth2-proxy / keycloak-clients
#                               -> keycloak, minio.adhar-system -> minio,
#                               kafka.strimzi.io / adhar-kafka -> kafka-operator,
#                               cert-manager.io/ -> cert-manager
#   * stability ............... curated-set enablement, the bar MARKETPLACE.md
#                               §3 states: enabled in BOTH appsets -> stable,
#                               in one -> beta, in neither -> alpha
#   * planeAffinity ........... control-plane, except the thin workload profile
#                               in adhar-appset-workload.yaml, which gets `any`,
#                               and any package shipping a DaemonSet (never
#                               control-plane; flagged for a maintainer pass)
#   * resources.localSafe ..... seeded from adhar-appset-local.yaml (a
#                               starting point for a footprint judgement,
#                               not a mirror of the curated core)
#
# Descriptions, licenses and homepages come from the curated table below —
# honest one-liners beat a scraped HTTPRoute comment. Packages missing from the
# table fall back to the README's first paragraph, then the leading comment
# block of manifests/install.yaml, then a generated default.
#
# Usage:
#   hack/gen-package-contracts.sh            # create missing contracts
#   hack/gen-package-contracts.sh --dry-run  # list what would be created
#
# Validate the result with: hack/validate-packages.sh
#
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if ! python3 -c 'import yaml' >/dev/null 2>&1; then
  echo "ERROR: python3 with PyYAML is required (pip install pyyaml)." >&2
  exit 2
fi

REPO_ROOT="${REPO_ROOT}" python3 - "$@" <<'PY'
import os
import re
import sys
import glob

import yaml

ROOT = os.environ["REPO_ROOT"]
PKG_DIR = os.path.join(ROOT, "platform", "stack", "packages")
STACK_DIR = os.path.join(ROOT, "platform", "stack")
DRY_RUN = "--dry-run" in sys.argv[1:]

MAINTAINER_NAME = "Adhar Platform Team"
ADHAR_REPO = "https://github.com/adhar-io/adhar"
MIN_ADHAR_VERSION = "0.1.0"

# ---------------------------------------------------------------------------
# Curated per-package metadata: description (one honest line), SPDX license of
# the UPSTREAM project, homepage, search keywords, and version/appVersion where
# the tree offers no reliable signal (packages with no generate-manifests.sh).
# ---------------------------------------------------------------------------
CURATED = {
 "ai/adhar-ai": dict(
   description="Agentic control layer (ADR-0024): per-domain MCP tool servers, a provider-agnostic LLM gateway, and an agent runtime whose writes land as Gitea PRs.",
   license="Apache-2.0", homepage=ADHAR_REPO, version="0.1.0", appVersion="latest",
   keywords="ai agent mcp llm gitops"),

 "application/adhar-libraries": dict(
   description="Tekton pipelines that build, test and release the platform's shared Maven and npm libraries into Nexus.",
   license="Apache-2.0", homepage=ADHAR_REPO, version="0.1.0",
   keywords="ci tekton maven npm release"),
 "application/argo-events": dict(
   description="Argo Events event-driven automation: event sources, sensors and triggers that start workflows from external events.",
   license="Apache-2.0", homepage="https://argoproj.github.io/argo-events/",
   keywords="events eventing automation argo"),
 "application/argo-rollout": dict(
   description="Argo Rollouts progressive-delivery controller (blue-green and canary) with its dashboard gated by Keycloak.",
   license="Apache-2.0", homepage="https://argo-rollouts.readthedocs.io",
   keywords="progressive-delivery canary blue-green argo"),
 "application/argo-workflows": dict(
   description="Argo Workflows container-native workflow engine for running CI and batch jobs as Kubernetes DAGs.",
   license="Apache-2.0", homepage="https://argo-workflows.readthedocs.io",
   keywords="workflows ci batch dag argo"),
 "application/baserow": dict(
   description="Baserow no-code database and spreadsheet UI, exposed through the platform Gateway with Keycloak SSO.",
   license="MIT", homepage="https://baserow.io",
   keywords="no-code database spreadsheet"),
 "application/buildpack": dict(
   description="kpack Cloud Native Buildpacks builder that turns application source into OCI images without Dockerfiles.",
   license="Apache-2.0", homepage="https://github.com/buildpacks-community/kpack",
   keywords="build buildpacks images kpack supply-chain"),
 "application/chaos-mesh": dict(
   description="Chaos Mesh chaos-engineering platform for injecting pod, network and IO faults into Kubernetes workloads.",
   license="Apache-2.0", homepage="https://chaos-mesh.org",
   keywords="chaos resilience testing fault-injection"),
 "application/coder": dict(
   description="Coder self-hosted cloud development environments, bootstrapped with an admin owner and Keycloak SSO.",
   license="AGPL-3.0", homepage="https://coder.com",
   keywords="cde remote-development workspaces"),
 "application/dapr": dict(
   description="Dapr distributed application runtime: service invocation, pub/sub, state and bindings delivered as sidecars.",
   license="Apache-2.0", homepage="https://dapr.io",
   keywords="runtime sidecar pubsub microservices"),
 "application/external-dns": dict(
   description="ExternalDNS controller that publishes Gateway and Service hostnames to the configured cloud DNS provider.",
   license="Apache-2.0", homepage="https://kubernetes-sigs.github.io/external-dns/",
   keywords="dns networking gateway records"),
 "application/harbor": dict(
   description="Harbor OCI registry with vulnerability scanning and replication; internal TLS is minted from the platform CA.",
   license="Apache-2.0", homepage="https://goharbor.io",
   keywords="registry oci images scanning supply-chain"),
 "application/k6": dict(
   description="Grafana k6 Operator that runs distributed load tests as Kubernetes jobs from TestRun resources.",
   license="AGPL-3.0", homepage="https://k6.io",
   keywords="load-testing performance k6"),
 "application/keda": dict(
   description="KEDA event-driven autoscaler that scales workloads from queue depth, streams and custom metrics.",
   license="Apache-2.0", homepage="https://keda.sh",
   keywords="autoscaling scaling events keda"),
 "application/knative": dict(
   description="Knative Operator for installing Knative Serving and Eventing (scale-to-zero serverless workloads).",
   license="Apache-2.0", homepage="https://knative.dev",
   keywords="serverless scale-to-zero eventing knative"),
 "application/n8n": dict(
   description="n8n workflow automation server for wiring integrations between the platform's services.",
   license="Sustainable-Use-License", homepage="https://n8n.io",
   keywords="automation workflows integrations"),
 "application/nexus": dict(
   description="Sonatype Nexus Repository OSS — the platform's Maven, npm and PyPI artefact repository (Harbor stays OCI-only).",
   license="EPL-1.0", homepage="https://www.sonatype.com/products/sonatype-nexus-repository",
   version="3.75.1", appVersion="3.75.1",
   keywords="artifacts maven npm repository"),
 "application/open-function": dict(
   description="OpenFunction FaaS platform for building and running serverless functions on Kubernetes.",
   license="Apache-2.0", homepage="https://openfunction.dev",
   keywords="faas serverless functions"),
 "application/penpot": dict(
   description="Penpot open-source design and prototyping tool, with in-namespace PostgreSQL and Valkey for local use.",
   license="MPL-2.0", homepage="https://penpot.app",
   keywords="design prototyping ux"),
 "application/plane": dict(
   description="Plane Community Edition project and issue tracking, routed through the platform Gateway.",
   license="AGPL-3.0", homepage="https://plane.so",
   keywords="project-management issues planning"),
 "application/posthog": dict(
   description="PostHog product analytics, backed by ClickHouse with ClickHouse Keeper and the platform's Strimzi Kafka.",
   license="MIT", homepage="https://posthog.com",
   keywords="analytics product events clickhouse"),
 "application/preview-environments": dict(
   description="Guardrails for ephemeral preview namespaces (ADR-0017): Kyverno rules that stamp every preview namespace with a ResourceQuota and a LimitRange.",
   license="Apache-2.0", homepage=ADHAR_REPO, version="0.1.0",
   keywords="preview ephemeral quota kyverno"),
 "application/scorecards": dict(
   description="Production-readiness scorer: a CronJob grades every service 0-100 from in-cluster signals and publishes the adhar-scorecards ConfigMap the Console reads.",
   license="Apache-2.0", homepage=ADHAR_REPO, version="0.1.0",
   keywords="scorecards readiness governance console"),
 "application/supply-chain": dict(
   description="The signed build path: one kpack builder and pipeline identity that pushes to Harbor and cosign-signs every image it produces.",
   license="Apache-2.0", homepage=ADHAR_REPO, version="0.1.0",
   keywords="supply-chain cosign kpack harbor signing"),
 "application/tekton": dict(
   description="Tekton Pipelines, Triggers and Dashboard — the platform's Kubernetes-native CI engine, dashboard gated by Keycloak.",
   license="Apache-2.0", homepage="https://tekton.dev",
   keywords="ci pipelines tekton build"),
 "application/tooljet": dict(
   description="ToolJet low-code platform for internal tools; self-contained with its own PostgreSQL and Redis.",
   license="AGPL-3.0", homepage="https://tooljet.com", version="0.1.0", appVersion="ce-latest",
   keywords="low-code internal-tools apps"),

 "core/Kamaji": dict(
   description="Kamaji hosted control-plane manager: tenant Kubernetes control planes run as pods on the management cluster.",
   license="Apache-2.0", homepage="https://kamaji.clastix.io",
   keywords="control-plane multi-tenancy clusters"),
 "core/adhar-console": dict(
   description="Adhar Console — the Backstage-based developer portal: software catalog, golden-path scaffolding, ArgoCD and policy views, cloud shell.",
   license="Apache-2.0", homepage="https://github.com/adhar-io/adhar-console",
   version="0.1.0", appVersion="latest",
   keywords="portal backstage catalog developer-experience"),
 "core/open-cluster-management": dict(
   description="Open Cluster Management cluster-manager (hub) for registering and governing fleets of Kubernetes clusters.",
   license="Apache-2.0", homepage="https://open-cluster-management.io",
   keywords="multicluster fleet hub ocm"),
 "core/sveltos": dict(
   description="Projectsveltos add-on controller that deploys and continuously enforces Kubernetes add-ons across managed clusters.",
   license="Apache-2.0", homepage="https://projectsveltos.github.io",
   keywords="multicluster addons deployment"),
 "core/vcluster": dict(
   description="vcluster virtual Kubernetes clusters, used as lightweight data planes inside the management cluster.",
   license="Apache-2.0", homepage="https://www.vcluster.com",
   keywords="virtual-cluster multi-tenancy data-plane"),
 "core/velero": dict(
   description="Velero backup and restore of platform Kubernetes state; the default backup schedules ship enabled.",
   license="Apache-2.0", homepage="https://velero.io",
   keywords="backup restore disaster-recovery"),

 "data/airbyte": dict(
   description="Airbyte ELT platform for syncing data from external sources into the platform's lakehouse.",
   license="Elastic-2.0", homepage="https://airbyte.com",
   keywords="elt ingestion data-integration"),
 "data/cnpg": dict(
   description="CloudNativePG operator — the platform's PostgreSQL: HA clusters, scheduled barman backups and a Prometheus exporter.",
   license="Apache-2.0", homepage="https://cloudnative-pg.io", appVersion="1.30.0",
   keywords="postgresql database operator cnpg"),
 "data/dagster": dict(
   description="Dagster orchestration for data assets and pipelines, with the webserver behind Keycloak SSO.",
   license="Apache-2.0", homepage="https://dagster.io",
   keywords="orchestration data-pipelines assets"),
 "data/jupyterhub": dict(
   description="JupyterHub multi-user notebook environment for data and ML work, gated by Keycloak.",
   license="BSD-3-Clause", homepage="https://jupyter.org/hub",
   keywords="notebooks jupyter data-science ml"),
 "data/kafka-operator": dict(
   description="Strimzi Kafka operator — the platform's KRaft Kafka clusters, topics and users declared as custom resources.",
   license="Apache-2.0", homepage="https://strimzi.io",
   keywords="kafka streaming messaging strimzi"),
 "data/kafka-ui": dict(
   description="Web console for browsing Kafka topics, consumer groups and messages on the platform's Strimzi cluster.",
   license="Apache-2.0", homepage="https://github.com/provectus/kafka-ui",
   version="0.7.2", appVersion="v0.7.2",
   keywords="kafka ui console streaming"),
 "data/kubeflow": dict(
   description="Kubeflow Pipelines for authoring and running ML workflows, backed by the platform's MinIO object store.",
   license="Apache-2.0", homepage="https://www.kubeflow.org",
   keywords="ml pipelines kubeflow training"),
 "data/lakefs": dict(
   description="lakeFS git-like version control over object storage: branches, commits and merges for data.",
   license="Apache-2.0", homepage="https://lakefs.io",
   keywords="data-versioning lakehouse object-storage"),
 "data/metabase": dict(
   description="Metabase BI and dashboards over the platform's databases, with Keycloak SSO and a CNPG-backed application database.",
   license="AGPL-3.0", homepage="https://www.metabase.com",
   version="0.51.10", appVersion="v0.51.10",
   keywords="bi dashboards analytics sql"),
 "data/minio": dict(
   description="MinIO S3-compatible object storage — the default bucket backend for Loki, Mimir, Velero and platform apps.",
   license="AGPL-3.0", homepage="https://min.io",
   keywords="object-storage s3 buckets minio"),
 "data/mongodb": dict(
   description="MongoDB Community Operator for running MongoDB replica sets as Kubernetes custom resources.",
   license="Apache-2.0", homepage="https://github.com/mongodb/mongodb-kubernetes-operator",
   keywords="mongodb nosql database operator"),
 "data/mysql-operator": dict(
   description="Oracle MySQL Operator for running InnoDB clusters and routers on Kubernetes.",
   license="GPL-2.0", homepage="https://github.com/mysql/mysql-operator",
   keywords="mysql database operator innodb"),
 "data/open-metadata": dict(
   description="OpenMetadata data catalog, lineage and governance, backed by CNPG PostgreSQL and OpenSearch.",
   license="Apache-2.0", homepage="https://open-metadata.org",
   keywords="catalog lineage governance metadata"),
 "data/opensearch": dict(
   description="OpenSearch and OpenSearch Dashboards — the platform's search and analytics store, with a Prometheus exporter.",
   license="Apache-2.0", homepage="https://opensearch.org",
   keywords="search analytics opensearch dashboards"),
 "data/prefect": dict(
   description="Prefect Server (OSS) for Python-native workflow orchestration; self-contained SQLite backend by default.",
   license="Apache-2.0", homepage="https://www.prefect.io",
   version="0.1.0", appVersion="3-latest",
   keywords="orchestration workflows python data"),
 "data/rabbitmq": dict(
   description="RabbitMQ message broker with the management UI behind Keycloak SSO.",
   license="MPL-2.0", homepage="https://www.rabbitmq.com",
   keywords="messaging queue amqp broker"),
 "data/redis": dict(
   description="ot-container-kit Redis operator plus a standalone Redis instance with the Prometheus redis_exporter sidecar.",
   license="Apache-2.0", homepage="https://ot-container-kit.github.io/redis-operator",
   keywords="redis cache operator key-value"),
 "data/rustfs": dict(
   description="RustFS Rust-based S3-compatible object store, offered alongside MinIO with a Keycloak-gated console.",
   license="Apache-2.0", homepage="https://rustfs.com",
   version="1.0.0-beta.8", appVersion="1.0.0-beta.8",
   keywords="object-storage s3 rustfs"),
 "data/spark-operator": dict(
   description="Kubeflow Spark Operator for submitting and managing Spark applications as Kubernetes custom resources.",
   license="Apache-2.0", homepage="https://github.com/kubeflow/spark-operator",
   keywords="spark batch analytics operator"),
 "data/trino": dict(
   description="Trino distributed SQL query engine over the lakehouse and the platform's databases.",
   license="Apache-2.0", homepage="https://trino.io",
   keywords="sql query-engine lakehouse federation"),
 "data/valkey": dict(
   description="Hyperspike Valkey operator — Redis-compatible caches as custom resources, with redis_exporter metrics.",
   license="BSD-3-Clause", homepage="https://valkey.io",
   keywords="valkey cache redis operator"),

 "infrastructure/crossplane": dict(
   description="Crossplane control plane (GitOps parity copy of the bootstrap install) for provisioning cloud infrastructure from Kubernetes.",
   license="Apache-2.0", homepage="https://crossplane.io",
   keywords="iac crossplane provisioning control-plane"),
 "infrastructure/terraform": dict(
   description="tf-controller (Flux IaC) running Terraform/OpenTofu plans as Kubernetes resources, with the Flux source controller.",
   license="Apache-2.0", homepage="https://flux-iac.github.io/tofu-controller",
   keywords="terraform opentofu iac flux"),

 "observability/beyla": dict(
   description="Grafana Beyla eBPF auto-instrumentation that emits RED metrics for workloads without touching their code.",
   license="Apache-2.0", homepage="https://grafana.com/oss/beyla-ebpf/",
   keywords="ebpf instrumentation metrics tracing"),
 "observability/faro": dict(
   description="Grafana Faro receiver (an Alloy instance) ingesting browser RUM telemetry through the platform Gateway.",
   license="Apache-2.0", homepage="https://grafana.com/oss/faro/",
   keywords="rum frontend telemetry browser"),
 "observability/headlamp": dict(
   description="Headlamp Kubernetes UI with native Keycloak OIDC, so cluster RBAC applies per signed-in user.",
   license="Apache-2.0", homepage="https://headlamp.dev",
   keywords="kubernetes ui dashboard oidc"),
 "observability/hubble": dict(
   description="Gateway route and SSO for the Hubble UI; Hubble relay and UI themselves ship with the bootstrap Cilium install.",
   license="Apache-2.0", homepage="https://docs.cilium.io/en/stable/overview/intro/",
   version="0.1.0",
   keywords="network-observability cilium hubble flows"),
 "observability/kube-prometheus": dict(
   description="kube-prometheus-stack: Prometheus Operator, Prometheus, Alertmanager and Grafana with the platform's dashboards.",
   license="Apache-2.0", homepage="https://github.com/prometheus-community/helm-charts",
   keywords="metrics prometheus grafana alerting"),
 "observability/loki-stack": dict(
   description="Grafana Loki log store (MinIO-backed) plus the ingestion route workload-cluster Alloy agents ship to.",
   license="AGPL-3.0", homepage="https://grafana.com/oss/loki/",
   keywords="logs loki storage observability"),
 "observability/metrics-server": dict(
   description="Kubernetes metrics-server supplying resource metrics for kubectl top and HorizontalPodAutoscalers.",
   license="Apache-2.0", homepage="https://github.com/kubernetes-sigs/metrics-server",
   keywords="metrics autoscaling resource-metrics"),
 "observability/mimir": dict(
   description="Grafana Mimir long-term metrics storage (MinIO-backed) and the remote-write endpoint for spoke clusters.",
   license="AGPL-3.0", homepage="https://grafana.com/oss/mimir/",
   keywords="metrics long-term-storage prometheus mimir"),
 "observability/oncall": dict(
   description="Grafana OnCall on-call scheduling, escalation chains and alert routing for the platform.",
   license="AGPL-3.0", homepage="https://grafana.com/oss/oncall/",
   keywords="oncall alerting escalation incident"),
 "observability/opencost": dict(
   description="OpenCost cost monitoring that attributes cluster spend per namespace, workload and label.",
   license="Apache-2.0", homepage="https://opencost.io",
   keywords="cost finops showback opencost"),
 "observability/pixie": dict(
   description="Pixie eBPF observability that auto-collects traces, metrics and logs with no instrumentation.",
   license="Apache-2.0", homepage="https://px.dev",
   keywords="ebpf observability tracing pixie"),
 "observability/pyroscope": dict(
   description="Grafana Pyroscope continuous profiling store, wired into Grafana as a profiles datasource.",
   license="AGPL-3.0", homepage="https://grafana.com/oss/pyroscope/",
   keywords="profiling continuous-profiling performance"),
 "observability/tempo": dict(
   description="Grafana Tempo distributed tracing backend and the OTLP ingestion route for spoke clusters.",
   license="AGPL-3.0", homepage="https://grafana.com/oss/tempo/",
   keywords="tracing otlp spans tempo"),
 "observability/victoria-metrics": dict(
   description="VictoriaMetrics single-node time-series database, an alternative metrics backend to Prometheus/Mimir.",
   license="Apache-2.0", homepage="https://victoriametrics.com",
   keywords="metrics tsdb victoriametrics"),

 "security/cert-manager": dict(
   description="cert-manager issues and renews the platform's X.509 certificates from Issuers, CAs and ACME.",
   license="Apache-2.0", homepage="https://cert-manager.io",
   keywords="tls certificates pki acme"),
 "security/cosign": dict(
   description="Sigstore policy-controller verifying cosign image signatures at admission against ClusterImagePolicies.",
   license="Apache-2.0", homepage="https://docs.sigstore.dev/policy-controller/overview/",
   keywords="cosign sigstore signatures admission supply-chain"),
 "security/credential-rotation": dict(
   description="One-shot Job that rotates the day-0 bootstrap credentials (Gitea and ArgoCD admin) into random break-glass secrets.",
   license="Apache-2.0", homepage=ADHAR_REPO, version="0.1.0",
   keywords="credentials rotation bootstrap break-glass"),
 "security/external-secrets": dict(
   description="External Secrets Operator syncing secrets from Vault and cloud secret managers into native Kubernetes Secrets.",
   license="Apache-2.0", homepage="https://external-secrets.io",
   keywords="secrets vault eso sync"),
 "security/falco": dict(
   description="Falco runtime threat detection, with Falcosidekick forwarding events and exposing Prometheus metrics.",
   license="Apache-2.0", homepage="https://falco.org",
   keywords="runtime-security threat-detection ebpf falco"),
 "security/keycloak": dict(
   description="Keycloak identity provider — the platform's OIDC issuer, realm, groups and per-application clients for SSO.",
   license="Apache-2.0", homepage="https://www.keycloak.org",
   version="26.7.1", appVersion="26.7.1",
   keywords="identity oidc sso keycloak"),
 "security/kubescape": dict(
   description="Kubescape operator for continuous posture scanning against CIS, NSA and MITRE control frameworks.",
   license="Apache-2.0", homepage="https://kubescape.io",
   keywords="posture compliance cis scanning"),
 "security/kyverno": dict(
   description="Kyverno policy engine: admission validation, mutation and generation, with PolicyReports for every result.",
   license="Apache-2.0", homepage="https://kyverno.io",
   keywords="policy admission governance kyverno"),
 "security/kyverno-policies": dict(
   description="The always-on Kyverno baseline: pod-security policies plus namespace plane-governance rules, all in Audit mode.",
   license="Apache-2.0", homepage="https://kyverno.io/policies/",
   keywords="policy baseline pod-security audit"),
 "security/policy-packs": dict(
   description="Opt-in CIS and SOC2 Kyverno profiles plus the ADR-0023 plane-isolation policy, all shipped in Audit mode.",
   license="Apache-2.0", homepage=ADHAR_REPO, version="0.1.0",
   keywords="compliance cis soc2 policy-pack"),
 "security/policy-reporter": dict(
   description="Policy Reporter UI aggregating Kyverno PolicyReports and Trivy VulnerabilityReports into one Keycloak-gated dashboard.",
   license="Apache-2.0", homepage="https://kyverno.github.io/policy-reporter/",
   version="2.20.1", appVersion="2.20.1",
   keywords="policy reports ui kyverno trivy"),
 "security/tetragon": dict(
   description="Cilium Tetragon eBPF runtime security observability and enforcement.",
   license="Apache-2.0", homepage="https://tetragon.io",
   keywords="runtime-security ebpf tetragon enforcement"),
 "security/vault": dict(
   description="HashiCorp Vault — the platform's secret backend, consumed by External Secrets so workloads see native Secrets.",
   license="BUSL-1.1", homepage="https://developer.hashicorp.com/vault",
   keywords="secrets vault kms encryption"),
}

# Packages whose manifests are vendored from a release URL rather than a Helm
# chart — recorded as an adharCompatibility note (upstreamChart stays empty).
VENDORED = {
 "application/argo-workflows": "https://github.com/argoproj/argo-workflows/releases",
 "application/buildpack": "https://github.com/buildpacks-community/kpack/releases",
 "application/knative": "https://github.com/knative/operator/releases",
 "application/tekton": "https://storage.googleapis.com/tekton-releases",
 "data/kubeflow": "https://github.com/kubeflow/pipelines/releases",
 "data/valkey": "https://github.com/hyperspike/valkey-operator/releases",
}

# Extra per-package notes appended to adharCompatibility.notes.
EXTRA_NOTES = {
 "core/Kamaji": "Directory is core/Kamaji (legacy capitalisation); the package/ArgoCD Application name is 'kamaji'.",
 "observability/loki-stack": "Directory is loki-stack; the ArgoCD Application name in the ApplicationSets is 'loki'.",
}

# Capability signals -> (dependency package name, category, optional).
# Keycloak is optional: it gates the UI (oauth2-proxy / OIDC), the workload
# itself still runs without it. The rest are hard requirements.
DEP_SIGNALS = [
 ("cnpg",             "data",     False, re.compile(r"postgresql\.cnpg\.io")),
 ("external-secrets", "security", False, re.compile(r"kind:\s*(Cluster)?ExternalSecret|external-secrets\.io/")),
 ("keycloak",         "security", True,  re.compile(r"adhar\.io/keycloak-client|oauth2-proxy|keycloak-clients")),
 ("minio",            "data",     False, re.compile(r"minio\.adhar-system")),
 ("kafka-operator",   "data",     False, re.compile(r"adhar-kafka|kafka\.strimzi\.io")),
 ("cert-manager",     "security", False, re.compile(r"cert-manager\.io/")),
]

VERSION_VARS = ["CHART_VERSION", "APP_VERSION", "OPERATOR_VERSION", "KPACK_VERSION",
                "PIPELINE_VERSION", "KFP_VERSION", "VERSION"]
SEMVER = re.compile(r"^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-.]+)?(?:\+[0-9A-Za-z-.]+)?$")

def is_package(path):
    return (os.path.isdir(os.path.join(path, "manifests"))
            or os.path.exists(os.path.join(path, "values.yaml"))
            or os.path.exists(os.path.join(path, "generate-manifests.sh")))


def discover():
    out = []
    for category in sorted(os.listdir(PKG_DIR)):
        cdir = os.path.join(PKG_DIR, category)
        if not os.path.isdir(cdir):
            continue
        for name in sorted(os.listdir(cdir)):
            pdir = os.path.join(cdir, name)
            if os.path.isdir(pdir) and is_package(pdir):
                out.append((category, name, pdir))
    return out


def appset_elements(path):
    """name/manifestPath/enabled for every list element in an ApplicationSet."""
    with open(path) as fh:
        doc = yaml.safe_load(fh)
    out = []
    for gen in doc["spec"]["generators"]:
        candidates = [gen] + gen.get("matrix", {}).get("generators", [])
        for cand in candidates:
            if "list" in cand:
                out.extend(cand["list"]["elements"])
    return out


def load_appsets():
    """key 'category/name' -> (local_enabled, prod_enabled); plus the `any` plane set."""
    enabled = {}
    for fname, idx in (("adhar-appset-local.yaml", 0), ("adhar-appset-production.yaml", 1)):
        path = os.path.join(STACK_DIR, fname)
        if not os.path.exists(path):
            continue
        for el in appset_elements(path):
            mpath = el.get("manifestPath", "")
            parts = mpath.split("/")
            if len(parts) < 2:
                continue
            key = "/".join(parts[:2])
            enabled.setdefault(key, ["false", "false"])[idx] = str(el.get("enabled", "false"))

    any_plane = set()
    wpath = os.path.join(STACK_DIR, "adhar-appset-workload.yaml")
    if os.path.exists(wpath):
        for el in appset_elements(wpath):
            parts = el.get("manifestPath", "").split("/")
            if len(parts) >= 2:
                any_plane.add("/".join(parts[:2]))
    return enabled, any_plane


_DAEMONSET_RE = re.compile(r"^\s*kind:\s*DaemonSet\s*$", re.MULTILINE)


def ships_daemonset(pdir):
    """True if the package's own manifests declare a DaemonSet.

    Charts pulled at sync time are invisible, so this can only under-detect.
    """
    for root, _, names in os.walk(pdir):
        for name in names:
            if name == "adhar-package.yaml" or not name.endswith((".yaml", ".yml", ".tmpl")):
                continue
            try:
                with open(os.path.join(root, name), errors="replace") as fh:
                    if _DAEMONSET_RE.search(fh.read()):
                        return True
            except OSError:
                continue
    return False


def read_gen_script(pdir):
    p = os.path.join(pdir, "generate-manifests.sh")
    return open(p, errors="ignore").read() if os.path.exists(p) else ""


def derive_version(script):
    for var in VERSION_VARS:
        m = re.search(rf"^{var}=(.+)$", script, re.M)
        if not m:
            continue
        val = m.group(1).split("#")[0].strip().strip("\"'")
        # VAR="${VAR:-1.2.3}" — take the default.
        default = re.match(r"^\$\{[A-Za-z_][A-Za-z0-9_]*:-([^}]*)\}$", val)
        if default:
            val = default.group(1).strip("\"'")
        if SEMVER.match(val):
            return val
    return None


def derive_upstream_chart(script):
    for line in script.splitlines():
        line = line.strip()
        if not line.startswith("helm template"):
            continue
        for tok in line.split():
            if tok.startswith("oci://"):
                return tok
            if (tok.count("/") == 1 and not tok.startswith("-")
                    and "=" not in tok and not tok.endswith((".yaml", ".yml", ".sh"))
                    and re.match(r"^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$", tok)):
                return tok
    return None


def package_blob(pdir):
    blob = []
    for root, _, files in os.walk(pdir):
        for f in files:
            if f.endswith((".yaml", ".yml", ".tmpl")):
                try:
                    blob.append(open(os.path.join(root, f), errors="ignore").read())
                except OSError:
                    pass
    return "\n".join(blob)


def derive_dependencies(name, blob):
    deps = []
    for dep, cat, optional, rx in DEP_SIGNALS:
        if dep != name and rx.search(blob):
            deps.append((dep, cat, optional))
    return deps


def fallback_description(category, name, pdir):
    """README first paragraph, else the leading comment block of install.yaml."""
    readme = os.path.join(pdir, "README.md")
    if os.path.exists(readme):
        para = []
        for line in open(readme, errors="ignore"):
            line = line.rstrip()
            if line.startswith("#") and not para:
                continue
            if not line.strip():
                if para:
                    break
                continue
            para.append(line.strip())
        if para:
            text = re.sub(r"\[([^]]+)\]\([^)]+\)", r"\1", " ".join(para))
            text = re.sub(r"[*`_]", "", text).strip()
            if len(text) >= 10:
                return text[:297].rsplit(" ", 1)[0] + "." if len(text) > 297 else text

    candidates = [os.path.join(pdir, "manifests", "install.yaml")]
    candidates += sorted(glob.glob(os.path.join(pdir, "manifests", "*.yaml")))
    for path in candidates:
        if not os.path.exists(path):
            continue
        head = []
        for line in open(path, errors="ignore"):
            line = line.rstrip()
            if line.startswith("#"):
                stripped = line.lstrip("#").strip()
                if stripped.lower().startswith(("this file is auto-generated", "source:", "generated")):
                    continue
                head.append(stripped)
            elif head or line.strip():
                break
        text = " ".join(h for h in head if h)
        if len(text) >= 40:
            return text[:297].rsplit(" ", 1)[0] + "." if len(text) > 297 else text

    pretty = name.replace("-", " ")
    return f"{pretty} platform package ({category}). Describe what it provides here."


def yscalar(value):
    """A single-line YAML scalar (safe_dump appends a '...' document end)."""
    return yaml.safe_dump(value, default_flow_style=True, width=10**6,
                          allow_unicode=True).split("\n")[0]


def quote(value):
    if re.match(r"^[A-Za-z0-9][A-Za-z0-9 ._/:+-]*$", value) and not value.endswith(" "):
        # Plain scalars are fine unless they could parse as something else.
        if value.lower() in ("true", "false", "null", "yes", "no", "on", "off") or SEMVER.match(value):
            return f'"{value}"'
        return value
    return yscalar(value)


def render(category, name, pdir, enabled, any_plane):
    key = f"{category}/{name}"
    curated = CURATED.get(key, {})
    script = read_gen_script(pdir)
    blob = package_blob(pdir)

    pkg_name = name.lower()
    version = curated.get("version") or derive_version(script) or "0.1.0"
    app_version = curated.get("appVersion")
    description = curated.get("description") or fallback_description(category, name, pdir)
    license_id = curated.get("license", "Apache-2.0")
    homepage = curated.get("homepage")
    keywords = (curated.get("keywords") or f"{category} {name.replace('-', ' ')}").split()

    # Stability follows the bar MARKETPLACE.md §3 already states — `stable` means
    # "enabled in curated sets" — so it is derived from which curated sets
    # actually enable the package, not from which directory it happens to live
    # in. The directory was the previous rule, and it produced contradictions:
    # `trivy` claimed stable while no profile enabled it, and `harbor` claimed
    # beta while both did.
    local_enabled, prod_enabled = enabled.get(key, ("false", "false"))
    local_on, prod_on = local_enabled == "true", prod_enabled == "true"
    if local_on and prod_on:
        # Exercised by both the Kind e2e run and every cloud run.
        stability = "stable"
    elif local_on or prod_on:
        # Exercised on one profile only.
        stability = "beta"
    else:
        # Wired but off everywhere: never exercised by default. The schema's
        # closest tier to "experimental" is alpha.
        stability = "alpha"

    # A package that ships a DaemonSet puts a pod on every node, workload nodes
    # included, so it is never purely control-plane. The tree cannot tell a pure
    # node agent (data-plane) from a control component with a node-agent half
    # (any), so scaffold the safe one and flag it for a maintainer — but never
    # emit control-plane, which is what validate-packages.sh now rejects.
    if key in any_plane:
        plane = "any"
    elif ships_daemonset(pdir):
        plane = "any                # review: 'data-plane' if this package IS the node agent"
    else:
        plane = "control-plane"

    notes = []
    if key in VENDORED:
        notes.append(f"Manifests vendored from {VENDORED[key]} (no upstream Helm chart).")
    if key in EXTRA_NOTES:
        notes.append(EXTRA_NOTES[key])
    if key not in enabled:
        notes.append("Not wired into an ApplicationSet yet.")

    upstream_chart = derive_upstream_chart(script)
    deps = derive_dependencies(pkg_name, blob)

    out = []
    add = out.append
    add(f"# Adhar marketplace contract for the {pkg_name} package.")
    add("# Validated against platform/stack/packages/marketplace.schema.json")
    add("# (run: hack/validate-packages.sh). See MARKETPLACE.md for the contract.")
    add("# Scaffolded by hack/gen-package-contracts.sh — refine it by hand as the")
    add("# package changes; the generator never overwrites an existing contract.")
    add("apiVersion: marketplace.adhar.io/v1alpha1")
    add("kind: AdharPackage")
    add(f"name: {pkg_name}")
    add(f"category: {category}")
    add(f"version: {quote(version)}")
    if app_version:
        add(f"appVersion: {quote(app_version)}")
    add(f"description: {yscalar(description)}")
    add("maintainer:")
    add(f"  name: {MAINTAINER_NAME}")
    add(f"  url: {ADHAR_REPO}")
    add("  firstParty: true")
    add(f"license: {quote(license_id)}")
    if homepage:
        add(f"homepage: {homepage}")
    add("adharCompatibility:")
    add(f"  minVersion: {quote(MIN_ADHAR_VERSION)}")
    if notes:
        add(f"  notes: {yscalar(' '.join(notes))}")
    if deps:
        add("dependencies:")
        for dep, cat, optional in deps:
            add(f"  - name: {dep}")
            add(f"    category: {cat}")
            if optional:
                add("    optional: true")
    else:
        add("dependencies: []")
    add("provenance:")
    add(f"  sourceRepo: {ADHAR_REPO}")
    if upstream_chart:
        add(f"  upstreamChart: {quote(upstream_chart)}")
    add("  signed: true                 # release artifacts are Cosign-keyless signed by the GoReleaser pipeline")
    add("  cosignKeyless: true          # Sigstore Fulcio + Rekor, ADR-0019")
    add("  scanned: true")
    add("  scanner: trivy")
    add(f"  sbom: {ADHAR_REPO}/releases  # SPDX SBOMs are attached to every release")
    add(f"planeAffinity: {plane}")
    add(f"stability: {stability}")
    add("resources:")
    # Seeded from the local gate because that is the only signal the tree has;
    # it is a footprint/safety judgement, though, not a mirror of the curated
    # core, so a reviewer may well set it true for a small package the local
    # budget leaves out (kargo, fluent-bit, trivy all are).
    add(f"  localSafe: {'true' if local_enabled == 'true' else 'false'}   # scaffolded from adhar-appset-local.yaml; review")
    add("keywords:")
    for kw in keywords:
        add(f"  - {kw}")
    return "\n".join(out) + "\n"


def main():
    enabled, any_plane = load_appsets()
    created = skipped = 0
    for category, name, pdir in discover():
        target = os.path.join(pdir, "adhar-package.yaml")
        rel = os.path.relpath(target, PKG_DIR)
        if os.path.exists(target):
            skipped += 1
            continue
        content = render(category, name, pdir, enabled, any_plane)
        if DRY_RUN:
            print(f"WOULD CREATE  {rel}")
        else:
            with open(target, "w") as fh:
                fh.write(content)
            print(f"CREATED  {rel}")
        created += 1
    print()
    print(f"{created} contract(s) {'would be created' if DRY_RUN else 'created'}, "
          f"{skipped} already present.")


main()
PY
