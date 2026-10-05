#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.16.5"

echo "# ONCALL INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/observability/oncall/generate-manifests.sh'" >>${INSTALL_YAML}


helm repo add grafana https://grafana.github.io/helm-charts --force-update
helm repo update
helm template --namespace adhar-system oncall grafana/oncall -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
# NOTE (2026-10-05): the committed install.yaml carries hand-applied fixes a
# plain re-render loses — probe timeoutSeconds raised from the chart's 1 s
# (TestNoProbeShipsAOneSecondTimeout), STABLE SECRET_KEY / MIRAGE_SECRET_KEY
# (the chart mints new ones on every render, which logs every user out), and
# `priorityClassName: adhar-node-bound` on the oncall-mariadb StatefulSet.
# Re-run only with those carried forward; diff against git before committing.
