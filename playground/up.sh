#!/bin/bash

# -----------------------------------------------------------------------------
# This script is used to bring up the Adhar playground using a Kind cluster.
#
# The script performs the following steps:
# 1. Creates a Kind cluster using a specified configuration file.
# 2. Sets the context to the new Kind cluster.
# 3. Installs the applications using Helm or kubectl.
#
# Exit immediately if a command exits with a non-zero status
set -e

# Create Kind cluster
echo "Creating Kind cluster..."
kind create cluster --config=manifests/kind/kind.yaml

# Set the context to the new Kind cluster
kubectl cluster-info --context kind-kind

# Create adhar-system namespace
echo "Creating adhar-system namespace..."
kubectl create namespace adhar-system


# Install Cilium
echo "Installing Cilium..."
kubectl apply -f manifests/cilium/install.yaml


# Install NGINX Ingress
echo "Installing NGINX Ingress Controller..."
kubectl apply -f manifests/nginx/install.yaml

# Install Gitea
echo "Installing Gitea..."
kubectl apply -f manifests/gitea/install.yaml


# Installing Crossplane
echo "Installing Crossplane..."
kubectl apply -f manifests/crossplane/install.yaml
# Install Crossplane packages
echo "Installing Crossplane packages..."
#kubectl apply -f manifests/crossplane/packages.yaml
# Install Crossplane providers
echo "Installing Crossplane providers..."
#kubectl apply -f manifests/crossplane/providers.yaml
# Install Crossplane configurations
echo "Installing Crossplane configurations..."
#kubectl apply -f manifests/crossplane/configurations.yaml
# Install Crossplane compositions
echo "Installing Crossplane compositions..."
#kubectl apply -f manifests/crossplane/compositions.yaml

# Apply additional Kubernetes manifests if needed
# echo "Applying additional Kubernetes manifests..."
# kubectl apply -f path/to/your/manifest.yaml

echo "Adhar playground is ready for your experiment!"