#!/bin/bash
set -e

# OpenFunction control plane (FaaS framework). No web UI of its own, so no
# HTTPRoute -- function ingress is created per-function. Installs into the
# shared adhar-system namespace like every other platform package (ADR-0011).
#
# NOTE: this package is DISABLED in every environment. It vendors Knative's
# ConfigMaps (config-defaults, config-tracing, ...) whose names are fixed
# upstream and collide with the core tekton package once both share
# adhar-system. Enabling open-function means disabling tekton first --
# see platform/stack/packages/CONFLICTS.md.

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="0.7.0"

echo "# OPEN-FUNCTION INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/open-function/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add openfunction https://openfunction.github.io/charts/ --force-update
helm repo update openfunction
helm template --namespace adhar-system openfunction openfunction/openfunction -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}

# A few namespace references are literals in the chart templates rather than
# {{ .Release.Namespace }} -- the CRD conversion-webhook clientConfigs and the
# helm-hook ServiceAccounts. Repoint them at adhar-system too, or the CRDs would
# route conversion to a Service in a namespace that no longer exists.
sed -i.bak 's/^\( *\)namespace: openfunction$/\1namespace: adhar-system/' ${INSTALL_YAML}
rm -f ${INSTALL_YAML}.bak

# NOT rewritten: the namespaces of the sub-stacks this chart vendors
# (dapr-system, keda, knative-serving, tekton-pipelines, shipwright-build,
# projectcontour) and the `kind: Namespace` objects it ships for them. Those are
# separate products bundled by the chart, several of which Adhar already runs as
# their own packages in adhar-system -- which is exactly why this package stays
# disabled. Do not enable it without untangling that first (CONFLICTS.md).
