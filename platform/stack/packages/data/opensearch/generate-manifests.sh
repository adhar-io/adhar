#!/bin/bash
set -e
#
# Renders the OpenSearch Kubernetes operator and the platform's shared cluster.
#
# Replaced the plain-chart install on 2026-10-03. This package used to render
# `opensearch/opensearch` into a StatefulSet and `opensearch/opensearch-dashboards`
# into a Deployment, with the admin password as a literal env var and every day-2
# operation — adding a node, taking a snapshot, creating a user, setting an index
# lifecycle — a manual REST call the platform had no record of. The operator makes
# all of those CRs that Argo CD owns and `kubectl get` can show.
#
# TWO charts, in this order:
#
#   1. opensearch-operator  — the 10 `opensearch.org` CRDs (wave -1) plus the
#      controller, its RBAC, its validating webhook and the self-signed
#      cert-manager Issuer/Certificate that serves it (wave 0).
#   2. opensearch-cluster   — the OpenSearchCluster CR and the OpensearchRole /
#      OpensearchUser / OpensearchUserRoleBinding / OpenSearchISMPolicy objects
#      that go with it (wave 5, after the controller exists to reconcile them).
#
# Unlike MariaDB's operator, upstream does NOT publish a separate CRD chart —
# `installCRDs: true` on the operator chart is the supported path — so the two
# render into one install.yaml and the sync waves do the ordering.
#
# Versions are pinned here and NOT floated: the OpenSearch engine version in
# values-cluster.yaml is coupled to the Prometheus exporter plugin release the
# operator downloads, so bumping the chart without checking that plugin leaves
# every node unable to start. See the comment on `general.version`.

INSTALL_YAML="manifests/install.yaml"
CLUSTER_YAML="manifests/cluster.yaml"

OPERATOR_VERSION="3.0.14"   # app 3.0.0 — the first stable OpenSearch 3.x operator
CLUSTER_VERSION="3.3.5"     # chart appVersion 3.4.0; values-cluster.yaml pins 3.8.0

helm repo add opensearch-operator https://opensearch-project.github.io/opensearch-k8s-operator/ --force-update
helm repo update opensearch-operator

# 1. Operator: CRDs + controller.
echo "# OPENSEARCH OPERATOR INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/data/opensearch/generate-manifests.sh'" >>${INSTALL_YAML}
echo "# Do not hand-edit: re-run the script and commit the result." >>${INSTALL_YAML}
helm template --include-crds --namespace adhar-system opensearch-operator \
  opensearch-operator/opensearch-operator -f values.yaml --version ${OPERATOR_VERSION} >>${INSTALL_YAML}

# 2. The shared cluster. The release name IS the cluster name, and every Service
#    name derives from it: `opensearch` (9200), `opensearch-nodes` (headless),
#    `opensearch-dashboards` (5601). Changing it renames the Services and breaks
#    manifests/httproute.yaml, manifests/sso.yaml and the consumers in
#    data/open-metadata and data/libredb-studio.
echo "# OPENSEARCH SHARED CLUSTER" >${CLUSTER_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/data/opensearch/generate-manifests.sh'" >>${CLUSTER_YAML}
echo "# Do not hand-edit: re-run the script and commit the result." >>${CLUSTER_YAML}
helm template --namespace adhar-system opensearch \
  opensearch-operator/opensearch-cluster -f values-cluster.yaml --version ${CLUSTER_VERSION} >>${CLUSTER_YAML}

# Stamp Argo CD sync waves.
#
# Helm has no notion of them, and the ordering is not optional: a CRD applied in
# the same wave as a CR that uses it is a race Argo CD loses about half the time
# ("no matches for kind ... ensure CRDs are installed first").
#
# Stamped through the YAML OBJECT MODEL, not by inserting text after `metadata:`.
# Text insertion produces a SECOND `annotations:` key on every object whose chart
# already set one, and YAML takes the last — so the wave is silently discarded.
# That is exactly how the first MariaDB attempt shipped 12 unstamped CRDs.
stamp_waves() {
  python3 - "$1" <<'PY'
import io, sys, yaml

CLUSTER_KINDS = (
    'OpenSearchCluster',
    'OpensearchRole',
    'OpensearchUser',
    'OpensearchUserRoleBinding',
    'OpensearchActionGroup',
    'OpensearchTenant',
    'OpensearchComponentTemplate',
    'OpensearchIndexTemplate',
    'OpenSearchISMPolicy',
    'OpensearchSnapshotPolicy',
)

path = sys.argv[1]
docs = [d for d in yaml.safe_load_all(io.open(path, encoding='utf-8')) if d]
for d in docs:
    meta = d.setdefault('metadata', {})
    ann = meta.setdefault('annotations', {})
    if 'argocd.argoproj.io/sync-wave' in ann:
        continue
    kind = d.get('kind')
    if kind == 'CustomResourceDefinition':
        # Ahead of everything, including the controller that watches them.
        wave = '-1'
    elif kind in CLUSTER_KINDS:
        # After the controller is Healthy. A CR applied while the operator is
        # still starting is admitted and then sits unreconciled, which Argo CD
        # reports as Progressing for the whole app.
        wave = '5'
    else:
        wave = '0'
    ann['argocd.argoproj.io/sync-wave'] = wave

header = []
for line in io.open(path, encoding='utf-8'):
    if not line.startswith('#'):
        break
    header.append(line)

with io.open(path, 'w', encoding='utf-8') as fh:
    fh.writelines(header)
    yaml.safe_dump_all(docs, fh, default_flow_style=False, sort_keys=False, width=4096)
PY
}

stamp_waves "${INSTALL_YAML}"
stamp_waves "${CLUSTER_YAML}"
