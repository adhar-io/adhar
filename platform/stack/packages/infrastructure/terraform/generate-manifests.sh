#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="v0.15.1"

echo "# TERRAFORM INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/infrastructure/terraform/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add terraform https://flux-iac.github.io/tofu-controller --force-update
helm repo update terraform
helm template --namespace adhar-system tf-controller terraform/tf-controller -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}

# tf-controller (tofu-controller) sources its Terraform from Flux `GitRepository` /
# `OCIRepository` / `Bucket` objects, i.e. it needs Flux's source-controller and its
# CRDs, which nothing else on the platform ships (GitOps is ArgoCD). Render only that
# controller from the flux2 chart; without it tf-controller crash-loops on
# "timed out waiting for cache to be synced for Kind *v1.GitRepository".
FLUX2_CHART_VERSION="2.19.0"
helm repo add fluxcd-community https://fluxcd-community.github.io/helm-charts --force-update
helm repo update fluxcd-community
echo "---" >>${INSTALL_YAML}
helm template --namespace adhar-system flux-source fluxcd-community/flux2 --version ${FLUX2_CHART_VERSION} --include-crds \
  --set installCRDs=true \
  --set sourceController.create=true \
  --set helmController.create=false \
  --set kustomizeController.create=false \
  --set notificationController.create=false \
  --set imageAutomationController.create=false \
  --set imageReflectionController.create=false \
  --set policies.create=false \
  --set rbac.create=true \
  --set-string crds.annotations."argocd\.argoproj\.io/sync-wave"="-1" >>${INSTALL_YAML}

# Version skew between the two charts above: tf-controller still renders its
# optional "primitive modules" OCIRepository at source.toolkit.fluxcd.io/v1beta2,
# while flux2 2.19's source-controller CRDs serve only v1. ArgoCD cannot apply it
# ("no matches for kind OCIRepository in version …/v1beta2") and retries the whole
# Application forever — seen live as `Retrying attempt #41`. The spec fields
# (interval, ref.tag, url) are unchanged between the two versions, so pin the
# object to the version the CRDs actually serve.
python3 - "${INSTALL_YAML}" <<'PYEOF'
import re, sys
path = sys.argv[1]
src = open(path).read()
# Only rewrite real objects, never the CRD schemas' own `apiVersion:` strings,
# which appear indented inside the OpenAPI definitions.
out, n = re.subn(r'(?m)^apiVersion: source\.toolkit\.fluxcd\.io/v1beta2$',
                 'apiVersion: source.toolkit.fluxcd.io/v1', src)
open(path, 'w').write(out)
print(f"repinned {n} source.toolkit.fluxcd.io object(s) from v1beta2 to v1")
PYEOF
