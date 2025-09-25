#!/bin/bash

# This script generates Hubble UI ingress manifests
# The manifests are static as they only configure ingress access to the existing Hubble UI service

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MANIFESTS_DIR="${SCRIPT_DIR}/manifests"

echo "📋 Generating Hubble UI ingress manifests..."

# Create manifests directory if it doesn't exist
mkdir -p "${MANIFESTS_DIR}"

# The ingress.yaml file is already created and doesn't need generation
# This script serves as documentation for the package

echo "✅ Hubble UI ingress manifests are ready"
echo "   - Ingress: ${MANIFESTS_DIR}/ingress.yaml"
echo "   - Access URL: https://adhar.localtest.me/hubble"
