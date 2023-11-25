#!/usr/bin/env bash

curl -LO "https://storage.googleapis.com/minikube/releases/latest/minikube-darwin-arm64"
chmod +x ./minikube-darwin-arm64
sudo mv ./minikube-darwin-arm64 ../bin/minikube
#sudo install minikube-darwin-arm64 /usr/local/bin/minikube
