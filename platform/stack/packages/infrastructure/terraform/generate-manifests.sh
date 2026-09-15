#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
# tofu-controller (the continuation of tf-controller under flux-iac). v0.15.x
# still watched Flux sources at source.toolkit.fluxcd.io/v1beta2, which the
# flux2 2.19 source-controller CRDs below no longer serve — the controller
# crash-looped forever on "timed out waiting for cache to be synced for Kind
# *v1beta2.Bucket" (2026-09-15 DO bring-up). 0.16.x watches v1.
CHART_VERSION="0.16.5"

echo "# TERRAFORM INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/infrastructure/terraform/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add terraform https://flux-iac.github.io/tofu-controller --force-update
helm repo update terraform
helm template --namespace adhar-system tf-controller terraform/tofu-controller -f values.yaml --version ${CHART_VERSION} --include-crds >>${INSTALL_YAML}

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

# Guard against version skew between the two charts: an older tf-controller
# rendered its optional "primitive modules" OCIRepository at
# source.toolkit.fluxcd.io/v1beta2, which flux2 2.19's CRDs do not serve, and
# ArgoCD retried the Application forever. tofu-controller 0.16 renders v1
# (the rewrite below reports 0), but keep the pin so a future chart bump
# cannot reintroduce it silently.
python3 - "${INSTALL_YAML}" <<'PYEOF'
import re, sys
path = sys.argv[1]
src = open(path).read()
# Only rewrite real objects, never the CRD schemas' own `apiVersion:` strings,
# which appear indented inside the OpenAPI definitions.
out, n = re.subn(r'(?m)^apiVersion: source\.toolkit\.fluxcd\.io/v1beta2$',
                 'apiVersion: source.toolkit.fluxcd.io/v1', src)
# The tofu-controller chart hardcodes the runner cache-encryption Secret into
# `flux-system` regardless of the release namespace; ArgoCD then fails the
# sync on a namespace that does not exist. Everything lives in adhar-system
# (ADR-0011), the chart's own runner ServiceAccount included.
out, m = re.subn(r'(?m)^  namespace: flux-system$', '  namespace: adhar-system', out)
open(path, 'w').write(out)
print(f"repinned {n} source.toolkit.fluxcd.io object(s) from v1beta2 to v1; {m} flux-system namespace(s) moved to adhar-system")
PYEOF
