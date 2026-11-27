#!/bin/sh
set -eu

if [ "$#" -ne 2 ]; then
  echo "usage: $0 OUTPUT_DIRECTORY VERSION" >&2
  exit 2
fi

output=$1
version=$2
rm -rf "$output"
mkdir -p "$output"

for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
  binary="$output/assistant-sandbox-${target%/*}-${target#*/}"
  if [ "${target%/*}" = windows ]; then
    binary="${binary}.exe"
  fi
  if [ "$target" = linux/amd64 ]; then
    CGO_ENABLED=1 go build -trimpath -tags netgo,osusergo \
      -ldflags "-linkmode external -extldflags -static -X main.version=$version" \
      -o "$binary" ./cmd/assistant-sandbox
  else
    CGO_ENABLED=0 GOOS="${target%/*}" GOARCH="${target#*/}" \
      go build -trimpath -ldflags "-X main.version=$version" \
      -o "$binary" ./cmd/assistant-sandbox
  fi
done

: > "$output/SBOM.txt"
for binary in "$output"/assistant-sandbox-*; do
  printf '== %s ==\n' "$(basename "$binary")" >> "$output/SBOM.txt"
  go version -m "$binary" >> "$output/SBOM.txt"
done
(
  cd "$output"
  sha256sum assistant-sandbox-* SBOM.txt > SHA256SUMS
)
