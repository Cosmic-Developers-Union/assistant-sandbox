#!/bin/sh
set -eu

if [ "$#" -ne 2 ]; then
  echo "usage: $0 DOWNLOAD_DIRECTORY VERSION" >&2
  exit 2
fi

directory=$1
version=$2
cd "$directory"

for asset in \
  assistant-sandbox-linux-amd64 \
  assistant-sandbox-linux-arm64 \
  assistant-sandbox-darwin-amd64 \
  assistant-sandbox-darwin-arm64 \
  assistant-sandbox-windows-amd64.exe \
  SBOM.txt \
  SHA256SUMS; do
  test -s "$asset"
  if [ "$asset" != SHA256SUMS ]; then
    grep -F " $asset" SHA256SUMS >/dev/null
  fi
done

sha256sum -c SHA256SUMS
grep -q 'assistant-sandbox' SBOM.txt

file assistant-sandbox-linux-amd64 | grep -F 'ELF 64-bit LSB executable, x86-64'
file assistant-sandbox-linux-arm64 | grep -F 'ARM aarch64'
file assistant-sandbox-darwin-amd64 | grep -E 'Mach-O 64-bit.*x86_64'
file assistant-sandbox-darwin-arm64 | grep -E 'Mach-O 64-bit.*arm64'
file assistant-sandbox-windows-amd64.exe | grep -E 'PE32\+.*x86-64'

actual=$(PATH=/nonexistent ./assistant-sandbox-linux-amd64 --version)
test "$actual" = "assistant-sandbox $version"
echo "release download verification passed: $version"
