#!/bin/bash

# Script to generate ArgoCD Application resources from local environment config
LOCAL_CONFIG="platform/stack/environments/local/config.yaml"
OUTPUT_DIR="platform/stack/generated-applications"

echo "Generating ArgoCD Application resources from local environment config..."

# Create output directory
mkdir -p "$OUTPUT_DIR"

# Function to generate application YAML
generate_application() {
    local name="$1"
    local namespace="$2"
    local category="$3"
    local manifest_path="$4"
    
    cat > "$OUTPUT_DIR/${name}-app.yaml" << EOF
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ${name}
  namespace: adhar-system
  finalizers:
    - resources-finalizer.argocd.argoproj.io
  labels:
    adhar.io/package-name: "${name}"
    adhar.io/category: "${category}"
    environment: "local"
spec:
  destination:
    namespace: "${namespace}"
    server: https://kubernetes.default.svc
  project: default
  sources:
    - helm:
        valueFiles:
          - values.yaml
      path: ${manifest_path}
      repoURL: http://gitea-http.adhar-system.svc.cluster.local:3000/gitea_admin/packages
      targetRevision: main
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    retry:
      backoff:
        duration: 5s
        factor: 2
        maxDuration: 3m0s
      limit: 30
    syncOptions:
      - Replace=true
      - CreateNamespace=true
      - ServerSideApply=true
EOF

    echo "  Generated: ${name}-app.yaml"
}

# Parse the YAML and generate applications
# This is a simple approach - in production you'd want to use a proper YAML parser
echo "Reading local environment configuration..."

# Extract package information using grep and sed
grep -A 4 "name:" "$LOCAL_CONFIG" | while read -r line; do
    if [[ $line =~ name:[[:space:]]*(.+)$ ]]; then
        name="${BASH_REMATCH[1]}"
        # Read the next few lines to get other fields
        read -r enabled_line
        read -r namespace_line
        read -r category_line
        read -r manifest_line
        
        if [[ $enabled_line =~ enabled:[[:space:]]*(.+)$ ]] && [[ ${BASH_REMATCH[1]} == "true" ]]; then
            if [[ $namespace_line =~ namespace:[[:space:]]*(.+)$ ]]; then
                namespace="${BASH_REMATCH[1]}"
            fi
            if [[ $category_line =~ category:[[:space:]]*(.+)$ ]]; then
                category="${BASH_REMATCH[1]}"
            fi
            if [[ $manifest_line =~ manifestPath:[[:space:]]*(.+)$ ]]; then
                manifest_path="${BASH_REMATCH[1]}"
            fi
            
            if [[ -n "$name" && -n "$namespace" && -n "$category" && -n "$manifest_path" ]]; then
                generate_application "$name" "$namespace" "$category" "$manifest_path"
            fi
        fi
    fi
done

echo "Application generation complete!"
echo "Total applications generated: $(ls -1 $OUTPUT_DIR/*.yaml 2>/dev/null | wc -l)"
echo "Output directory: $OUTPUT_DIR"
