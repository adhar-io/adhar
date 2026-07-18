#!/bin/bash
set -e

# Strimzi Kafka operator — the CRD-based operator (Kafka, KafkaTopic, KafkaUser,
# KafkaConnect, …) for running Apache Kafka on Kubernetes. We install only the
# cluster operator (watching all namespaces); actual Kafka clusters are created
# by applying Kafka CRs. This keeps the local footprint to a single lightweight
# operator Deployment.
INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.1.0"

echo "# KAFKA OPERATOR (STRIMZI) INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/data/kafka-operator/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add strimzi https://strimzi.io/charts/ --force-update
helm repo update strimzi
helm template strimzi-cluster-operator strimzi/strimzi-kafka-operator \
  --namespace adhar-system \
  --version ${CHART_VERSION} \
  --include-crds \
  --set watchAnyNamespace=true \
  --set resources.requests.cpu=50m \
  --set resources.requests.memory=192Mi \
  --set resources.limits.memory=384Mi \
  >>${INSTALL_YAML}
