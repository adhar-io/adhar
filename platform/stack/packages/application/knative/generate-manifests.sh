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
OPERATOR_VERSION="v1.23.1"

echo "# KNATIVE OPERATOR INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/knative/generate-manifests.sh'" >>${INSTALL_YAML}
echo "# Knative Operator ${OPERATOR_VERSION}" >>${INSTALL_YAML}

curl -sSL "https://github.com/knative/operator/releases/download/knative-${OPERATOR_VERSION}/operator.yaml" >>${INSTALL_YAML}

# Adhar runs packages in a single namespace (adhar-system). Newer upstream
# operator bundles default to knative-operator namespace and include a matching
# Namespace object; rewrite all namespace references and drop that extra
# namespace resource.
sed -i.bak 's/^\( *\)namespace: knative-operator$/\1namespace: adhar-system/' "${INSTALL_YAML}"
rm -f "${INSTALL_YAML}.bak"

awk '
function flush_doc() {
  if (doc == "") {
    return
  }
  if (doc ~ /\nkind: Namespace\n/ && doc ~ /\nmetadata:\n/ && doc ~ /\n  name: knative-operator\n/) {
    doc = ""
    return
  }
  printf "%s", doc
  doc = ""
}
{
  if ($0 == "---") {
    flush_doc()
    doc = "---\n"
    next
  }
  doc = doc $0 "\n"
}
END {
  flush_doc()
}
' "${INSTALL_YAML}" > "${INSTALL_YAML}.tmp"
mv "${INSTALL_YAML}.tmp" "${INSTALL_YAML}"
