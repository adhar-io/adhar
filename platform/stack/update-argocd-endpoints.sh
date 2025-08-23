#!/bin/bash
# Update ArgoCD Endpoints Script
# This script provides a way to update ArgoCD repository endpoints
# when Gitea services change, ensuring resilience

set -euo pipefail

NAMESPACE="adhar-system"
CONFIGMAP_NAME="gitea-argocd-config"

echo "🔧 Updating ArgoCD endpoints for resilience..."

# Get current Gitea service IP
CURRENT_IP=$(kubectl get svc gitea-http-clusterip -n $NAMESPACE -o jsonpath='{.spec.clusterIP}')
echo "📡 Current Gitea service IP: $CURRENT_IP"

# Update ConfigMap with current service information
kubectl patch configmap $CONFIGMAP_NAME -n $NAMESPACE --type='merge' -p="{\"data\":{\"gitea-service-ip\":\"$CURRENT_IP\"}}"

echo "✅ ConfigMap updated with current service IP"

# Restart ArgoCD repo-server to pick up changes
echo "🔄 Restarting ArgoCD repo-server..."
kubectl rollout restart deployment argo-cd-argocd-repo-server -n $NAMESPACE

echo "⏳ Waiting for ArgoCD repo-server to be ready..."
kubectl wait --for=condition=available --timeout=60s deployment/argo-cd-argocd-repo-server -n $NAMESPACE

echo "✅ ArgoCD endpoints updated successfully!"
echo "📋 Current endpoints:"
kubectl get configmap $CONFIGMAP_NAME -n $NAMESPACE -o yaml
