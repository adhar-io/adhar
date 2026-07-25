#!/bin/bash
# This package is HAND-MAINTAINED — do not regenerate from the bitnami chart.
# bitnami/mongodb pins docker.io/bitnamilegacy/mongodb images, which are
# amd64-only and crashloop on arm64 nodes (Apple Silicon Kind). The manifest in
# manifests/install.yaml uses the official multi-arch docker.io/library/mongo
# image instead. To bump the version, edit the image tag there directly.
echo "mongodb is hand-maintained; edit manifests/install.yaml directly." >&2
exit 0
