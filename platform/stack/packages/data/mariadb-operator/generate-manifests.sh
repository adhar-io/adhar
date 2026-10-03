#!/bin/bash
set -e
#
# Renders the MariaDB operator and the platform's shared MariaDB instance.
#
# Replaces `data/mysql-operator` (disabled 2026-10-03). MariaDB speaks the MySQL
# wire protocol, so anything that wanted MySQL is served, and this operator is the
# one under active release — 26.10.1 — with users, grants, databases, backups and
# Galera expressed as CRDs rather than as shell in an init container.
#
# THREE charts, in this order, and the order is the whole trick:
#
#   1. mariadb-operator-crds  — CRDs only. A chart that installs CRDs *and* the
#      controller in one release makes Argo CD race itself on a fresh cluster: the
#      controller's own CRs are applied in the same wave as the definitions they
#      need. Upstream ships them separately for exactly this reason.
#   2. mariadb-operator       — the controller, with crds.enabled=false so it never
#      owns the CRDs it was just given.
#   3. mariadb-cluster        — the shared instance, as a MariaDB CR.
#
# Sync waves are stamped by the generator (see WAVE_* below) rather than left to
# chart defaults, because Argo CD orders by wave and the CRDs must land first.

INSTALL_YAML="manifests/install.yaml"
CLUSTER_YAML="manifests/cluster.yaml"

CRDS_VERSION="26.10.1"
OPERATOR_VERSION="26.10.1"
CLUSTER_VERSION="26.10.1"

helm repo add mariadb-operator https://helm.mariadb.com/mariadb-operator --force-update
helm repo update mariadb-operator

echo "# MARIADB OPERATOR INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/data/mariadb-operator/generate-manifests.sh'" >>${INSTALL_YAML}
echo "# Do not hand-edit: re-run the script and commit the result." >>${INSTALL_YAML}

# 1. CRDs, wave -1: before anything that could reference them.
helm template --namespace adhar-system mariadb-operator-crds \
  mariadb-operator/mariadb-operator-crds --version ${CRDS_VERSION} >>${INSTALL_YAML}

# 2. The controller, wave 0.
echo "---" >>${INSTALL_YAML}
helm template --namespace adhar-system mariadb-operator \
  mariadb-operator/mariadb-operator -f values.yaml --version ${OPERATOR_VERSION} >>${INSTALL_YAML}

# 3. The shared instance, wave 5: after the controller is up to reconcile it.
echo "# MARIADB SHARED INSTANCE" >${CLUSTER_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/data/mariadb-operator/generate-manifests.sh'" >>${CLUSTER_YAML}
helm template --namespace adhar-system mariadb \
  mariadb-operator/mariadb-cluster -f values-cluster.yaml --version ${CLUSTER_VERSION} >>${CLUSTER_YAML}

# Stamp Argo CD sync waves.
#
# Helm has no notion of them, and the ordering is not optional: a CRD applied in
# the same wave as a CR that uses it is a race Argo CD loses about half the time
# ("no matches for kind ... ensure CRDs are installed first") — exactly how the
# Civo CSI broke a cluster on 2026-10-02.
#
# Stamped through the YAML OBJECT MODEL, not by inserting text after `metadata:`.
# The first version did the latter and produced two `annotations:` keys on every
# CRD, because the chart already sets one; YAML takes the last, so every CRD
# silently kept its chart annotations and got no wave at all.
stamp_waves() {
  python3 - "$1" <<'PY'
import io, sys, yaml

path = sys.argv[1]
docs = [d for d in yaml.safe_load_all(io.open(path, encoding='utf-8')) if d]
for d in docs:
    meta = d.setdefault('metadata', {})
    ann = meta.setdefault('annotations', {})
    if 'argocd.argoproj.io/sync-wave' in ann:
        continue
    # CRDs ahead of everything; the shared instance after the controller.
    if d.get('kind') == 'CustomResourceDefinition':
        wave = '-1'
    elif d.get('kind') in ('MariaDB', 'Database', 'User', 'Grant', 'Backup', 'PhysicalBackup', 'MaxScale'):
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
