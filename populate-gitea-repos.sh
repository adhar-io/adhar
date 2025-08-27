#!/bin/bash

set -e

echo "🚀 Populating Gitea repositories with package content..."

# Get Gitea pod name
GITEA_POD=$(kubectl get pods -n adhar-system -l app=gitea -o jsonpath='{.items[0].metadata.name}')
echo "📦 Using Gitea pod: $GITEA_POD"

# Function to populate packages repository
populate_packages_repo() {
    echo "📦 Populating packages repository..."
    
    # Clean up any existing working directory
    kubectl exec -n adhar-system "$GITEA_POD" -- rm -rf /tmp/packages-working
    
    # Clone the existing repository
    echo "  📁 Cloning existing packages repository..."
    kubectl exec -n adhar-system "$GITEA_POD" -- git clone /data/git/gitea-repositories/gitea_admin/packages.git /tmp/packages-working
    
    # Remove all existing content
    kubectl exec -n adhar-system "$GITEA_POD" -- rm -rf /tmp/packages-working/*
    
    # Copy the packages content
    echo "  📁 Copying packages content to working directory..."
    kubectl cp platform/stack/packages adhar-system/"$GITEA_POD":/tmp/packages-working/
    
    # Configure git
    kubectl exec -n adhar-system "$GITEA_POD" -- git -C /tmp/packages-working config user.name "Adhar Platform"
    kubectl exec -n adhar-system "$GITEA_POD" -- git -C /tmp/packages-working config user.email "admin@adhar.io"
    
    # Add all files
    kubectl exec -n adhar-system "$GITEA_POD" -- git -C /tmp/packages-working add .
    
    # Commit
    kubectl exec -n adhar-system "$GITEA_POD" -- git -C /tmp/packages-working commit -m "Update: Add all platform packages"
    
    # Push changes
    kubectl exec -n adhar-system "$GITEA_POD" -- git -C /tmp/packages-working push origin main
    
    echo "✅ Packages repository populated successfully!"
}

# Function to populate environments repository
populate_environments_repo() {
    echo "🌍 Populating environments repository..."
    
    # Clean up any existing working directory
    kubectl exec -n adhar-system "$GITEA_POD" -- rm -rf /tmp/environments-working
    
    # Clone the existing repository
    echo "  📁 Cloning existing environments repository..."
    kubectl exec -n adhar-system "$GITEA_POD" -- git clone /data/git/gitea-repositories/gitea_admin/environments.git /tmp/environments-working
    
    # Remove all existing content
    kubectl exec -n adhar-system "$GITEA_POD" -- rm -rf /tmp/environments-working/*
    
    # Copy the environments content
    echo "  📁 Copying environments content to working directory..."
    kubectl cp platform/stack/environments adhar-system/"$GITEA_POD":/tmp/environments-working/
    
    # Configure git
    kubectl exec -n adhar-system "$GITEA_POD" -- git -C /tmp/environments-working config user.name "Adhar Platform"
    kubectl exec -n adhar-system "$GITEA_POD" -- git -C /tmp/environments-working config user.email "admin@adhar.io"
    
    # Add all files
    kubectl exec -n adhar-system "$GITEA_POD" -- git -C /tmp/environments-working add .
    
    # Commit
    kubectl exec -n adhar-system "$GITEA_POD" -- git -C /tmp/environments-working commit -m "Update: Add environment configurations"
    
    # Push changes
    kubectl exec -n adhar-system "$GITEA_POD" -- git -C /tmp/environments-working push origin main
    
    echo "✅ Environments repository populated successfully!"
}

# Main execution
echo "🔄 Starting repository population..."

# Populate both repositories
populate_packages_repo
populate_environments_repo

echo "🎉 All Gitea repositories populated successfully!"
echo "📋 You can now check the repositories in Gitea UI or via git commands"
