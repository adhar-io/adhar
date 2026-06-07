#!/bin/bash
set -e

# Knative is installed via the Knative Operator, which is distributed as a
# static release manifest (not a Helm chart). We pin to a Knative Operator
# release and vendor its operator.yaml. Knative Serving/Eventing CRs are then
# created declaratively to let the operator install the data plane.
#
# Networking: the Knative Operator itself exposes no ingress. When Serving is
# configured it uses its own networking layer (Kourier/Istio), NOT nginx.

INSTALL_YAML="manifests/operator.yaml"
OPERATOR_VERSION="v1.22.2"

echo "# KNATIVE OPERATOR INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/knative/generate-manifests.sh'" >>${INSTALL_YAML}
echo "# Knative Operator ${OPERATOR_VERSION}" >>${INSTALL_YAML}

curl -sSL "https://github.com/knative/operator/releases/download/knative-${OPERATOR_VERSION}/operator.yaml" >>${INSTALL_YAML}
