#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="0.5.6"

echo "# SUPABASE INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/supabase/generate-manifests.sh'" >>${INSTALL_YAML}

# Community chart maintained by supabase-community/supabase-kubernetes.
helm repo add supabase https://supabase-community.github.io/supabase-kubernetes/ --force-update
helm repo update
helm template --namespace supabase supabase supabase/supabase -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}
