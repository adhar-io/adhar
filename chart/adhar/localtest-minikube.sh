#!/bin/bash

set -e

#####
# recommended options to start minikube
#####
# minikube start --driver docker --network minikube --kubernetes-version=v1.27.0 --cpus=max --memory=max
# docker network inspect minikube

#####
# configure minikube to connect from otomi
#####
KUBECONFIG_MINIKUBE=~/.kube/minikube-flattened-config
kubectl config view --flatten=true >$KUBECONFIG_MINIKUBE
export KUBECONFIG=$KUBECONFIG_MINIKUBE
kubectl config set-cluster minikube --server=https://minikube:8443 --insecure-skip-tls-verify
export CUSTOM_NETWORK='--network minikube'

#####
# apply
#####
binzx/otomi apply-as-apps -f helmfile.tpl/helmfile-init.yaml
binzx/otomi apply-as-apps
# minikube tunnel