#!/bin/bash

set -e -o pipefail
# get the latest stable release by look for tag name pattern like 'v*.*.*'.  For example, v1.1.1
# GitHub API returns releases in chronological order so we take the first matching tag name.
version=$(curl -s https://api.github.com/repos/adhar-io/adhar/releases | grep tag_name | grep -o -e '"v[0-9].[0-9].[0-9]"' | head -n1 | sed 's/"//g')

echo "Downloading adhar version ${version}"
curl -L --progress-bar -o ./adhar.tar.gz "https://github.com/adhar-io/adhar/releases/download/${version}/adhar-$(uname | awk '{print tolower($0)}')-$(uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/').tar.gz"
tar xzf adhar.tar.gz

echo "Moving adhar binary to /usr/local/bin"
sudo mv ./adhar /usr/local/bin/
adhar version
echo "Successfully installed Adhar CLI!"
