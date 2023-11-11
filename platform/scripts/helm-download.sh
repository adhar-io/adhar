#!/usr/bin/env bash

curl -fsSL -o get_helm.sh https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3
chmod 700 get_helm.sh
sh ./get_helm.sh
#chmod +x ./helm
#sudo mv ./helm ../bin/helm