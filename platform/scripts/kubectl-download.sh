#!/usr/bin/env bash

curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/darwin/arm64/kubectl"
chmod +x ./kubectl
sudo mv ./kubectl ../bin/kubectl
#sudo mv ./kubectl /usr/local/bin/kubectl