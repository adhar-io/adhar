#!/bin/bash
set -e -o pipefail

# Latest stable release tag (vX.Y.Z; prereleases excluded by /releases/latest)
version=$(curl -fsSL https://api.github.com/repos/adhar-io/adhar/releases/latest \
  | grep '"tag_name"' | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | head -n1)
if [ -z "$version" ]; then
  echo "Could not determine the latest adhar release" >&2
  exit 1
fi

os=$(uname | tr '[:upper:]' '[:lower:]')
arch=$(uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
asset="adhar-${version#v}-${os}-${arch}.tar.gz"
url="https://github.com/adhar-io/adhar/releases/download/${version}/${asset}"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading adhar ${version} (${os}/${arch})"
curl -fL --progress-bar -o "$tmp/adhar.tar.gz" "$url"
tar xzf "$tmp/adhar.tar.gz" -C "$tmp"

install_dir="${INSTALL_DIR:-/usr/local/bin}"
echo "Installing adhar to ${install_dir}"
if [ -w "$install_dir" ]; then
  mv "$tmp/adhar" "$install_dir/adhar"
else
  sudo mv "$tmp/adhar" "$install_dir/adhar"
fi

"$install_dir/adhar" version
echo "Successfully installed Adhar CLI!"
