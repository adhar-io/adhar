#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
# The platform controller embeds an identical copy for the imperative HA
# bootstrap (platform/controllers/adharplatform/cnpg.go). Both are applied
# with server-side apply under different field managers, so any drift
# between them makes the controller and Argo CD flip the operator back and
# forth — every flip restarts every CNPG database on the platform. Always
# regenerate both from here; never hand-edit either copy.
CONTROLLER_COPY="../../../../controllers/adharplatform/resources/cnpg/install.yaml"
CHART_VERSION="0.29.0"  # CloudNativePG operator 1.30.0

echo "# CLOUDNATIVE-PG INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/data/cnpg/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add cnpg https://cloudnative-pg.github.io/charts --force-update
helm repo update cnpg
helm template --namespace adhar-system cnpg cnpg/cloudnative-pg -f values.yaml --version ${CHART_VERSION} --kube-version 1.31.0 --include-crds >>${INSTALL_YAML}

cp "${INSTALL_YAML}" "${CONTROLLER_COPY}"
echo "wrote ${INSTALL_YAML} and ${CONTROLLER_COPY}"
