#!/bin/sh
# Reproduce the kernel capability boundary used by the linux/amd64 backend.
set -eu
export LC_ALL=C

if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
    echo "this probe requires linux/amd64" >&2
    exit 1
fi
for tool in cc readelf unshare; do
    command -v "$tool" >/dev/null || { echo "missing tool: $tool" >&2; exit 1; }
done

directory=$(mktemp -d "${TMPDIR:-/tmp}/assistant-native-probe.XXXXXX")
trap 'rm -rf "$directory"' EXIT HUP INT TERM
cc -static -O2 -s -o "$directory/probe" "$(dirname "$0")/namespace_probe.c"

echo "== minimal guest ELF =="
readelf -h "$directory/probe" | grep -E 'Class:|Data:|Type:|Machine:'

echo "== host namespaces =="
for name in user mnt pid net ipc uts cgroup; do
    printf '%s=' "$name"
    readlink "/proc/self/ns/$name"
done

echo "== isolated namespaces =="
unshare --user --map-root-user --mount --pid --fork --net --ipc --uts --cgroup \
    sh -c 'mount -t proc proc /proc; exec "$1"' sh "$directory/probe"

echo "== missing capability (unprivileged net namespace without user namespace) =="
if unshare --net "$directory/probe" >"$directory/negative.out" 2>"$directory/negative.err"; then
    echo "unshare --net unexpectedly succeeded; negative capability probe is not applicable" >&2
    exit 1
fi
cat "$directory/negative.err"
echo "negative probe failed as expected"
