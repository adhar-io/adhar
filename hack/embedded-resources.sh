#!/bin/bash

DIRECTORIES='argocd cilium crossplane gateway-api gitea'

for dir in $DIRECTORIES; do
    ./hack/$dir/generate-manifests.sh;
    if [[ $? -ne 0 ]]; then
        echo "Error running script: ./hack/$dir/generate-manifests.sh"
        exit 1
    fi
done