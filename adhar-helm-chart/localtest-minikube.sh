#!/bin/bash

set -e

#####
# recommended options to start minikube
#####
# minikube start --driver docker --network minikube --kubernetes-version=v1.28.0 --cpus=max --memory=max
# docker network inspect minikube

#####
# configure minikube to connect from adhar platform
#####
KUBECONFIG_MINIKUBE=~/.kube/minikube-flattened-config
kubectl config view --flatten=true > $KUBECONFIG_MINIKUBE
export KUBECONFIG=$KUBECONFIG_MINIKUBE
kubectl config set-cluster minikube --server=https://minikube:8443 --insecure-skip-tls-verify
export CUSTOM_NETWORK='--network minikube'

#####
# apply 
#####
../platform/bin/otomi apply

# minikube tunnel