#!/bin/bash

# Update all core tools by triggering their individual scripts
HACK_DIR="$(dirname "$0")"

# Trigger individual update scripts
bash "$HACK_DIR/crossplane/update-crossplane.sh" || exit 1
bash "$HACK_DIR/argocd/update-argocd.sh" || exit 1
bash "$HACK_DIR/cilium/update-cilium.sh" || exit 1
bash "$HACK_DIR/gitea/update-gitea.sh" || exit 1

echo "All core tools have been updated successfully."