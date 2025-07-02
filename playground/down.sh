#!/bin/bash

# Exit immediately if a command exits with a non-zero status
set -e

# Delete Kind cluster
echo "Deleting Kind cluster..."
kind delete cluster

echo "Kind cluster has been deleted."