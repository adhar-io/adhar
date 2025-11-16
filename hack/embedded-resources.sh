#!/bin/bash

DIRECTORIES='argocd gitea ingress-nginx cilium crossplane'

for dir in $DIRECTORIES; do
    ./hack/$dir/generate-manifests.sh;
    if [[ $? -ne 0 ]]; then
        echo "error running script: ./hack/$dir/generate-manifests.sh"
        exit 1
    fi
done