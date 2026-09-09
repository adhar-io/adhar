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
