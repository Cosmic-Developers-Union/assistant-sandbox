#!/bin/sh
# Build the fixed linux/amd64 guest root filesystem from pinned APKs.
set -eu
export LC_ALL=C

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
output=${1:-"$here/rootfs-amd64.tar.gz"}
case "$output" in /*) ;; *) output="$(pwd)/$output" ;; esac
base=alpine@sha256:9db2934886712d877c932e07249c75a59e6ca27adfbccc05261987a17bef2d63
repository=https://dl-cdn.alpinelinux.org/alpine/v3.22/main/x86_64

for tool in docker curl sha256sum tar gzip go; do
    command -v "$tool" >/dev/null || { echo "missing tool: $tool" >&2; exit 1; }
done
work=$(mktemp -d "${TMPDIR:-/tmp}/assistant-hostroot.XXXXXX")
container=
cleanup() {
    if [ -n "$container" ]; then docker rm -f "$container" >/dev/null 2>&1 || true; fi
    rm -rf "$work"
}
trap cleanup EXIT HUP INT TERM
mkdir -p "$work/packages" "$work/rootfs" "$(dirname "$output")"
repository_root=$(CDPATH= cd -- "$here/../.." && pwd)
(cd "$repository_root" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
    -ldflags '-s -w -buildid=' -o "$work/assistant-init" ./guest/hostroot/init)

while read -r digest name; do
    [ -n "$digest" ] || continue
    curl -fsSL "$repository/$name" -o "$work/packages/$name"
    printf '%s  %s\n' "$digest" "$work/packages/$name" | sha256sum -c - >/dev/null
done < "$here/APKS.sha256"

container=$(docker create --network none --hostname sandbox \
    -v "$work/packages:/packages:ro" -v "$work/assistant-init:/assistant-init:ro" "$base" sh -c '
        apk add --no-network --allow-untrusted /packages/*.apk >/dev/null
        cp /assistant-init /usr/bin/assistant-init
        chmod 0755 /usr/bin/assistant-init
        rm -rf /var/cache/apk/*
        mkdir -p /work /tmp /dev /proc /sys /root
        chmod 1777 /tmp
        printf "sandbox\n" > /etc/hostname
        printf "127.0.0.1 localhost\n" > /etc/hosts
        : > /etc/resolv.conf
    ')
docker start -a "$container" >/dev/null
docker export "$container" -o "$work/export.tar"
tar -xf "$work/export.tar" -C "$work/rootfs"
rm -f "$work/rootfs/.dockerenv"

tar --sort=name --format=gnu --mtime=@0 --owner=0 --group=0 --numeric-owner \
    -cf - -C "$work/rootfs" . | gzip -n -9 > "$output.tmp"
mv "$output.tmp" "$output"
digest=$(sha256sum "$output" | awk '{print $1}')
size=$(wc -c < "$output" | tr -d ' ')
init_digest=$(sha256sum "$work/assistant-init" | awk '{print $1}')
cat > "$output.manifest" <<EOF
format=assistant-hostroot-v1
base=$base
archive_sha256=$digest
archive_bytes=$size
init_sha256=$init_digest
node=22.23.5-r0
python=3.12.16-r0
bash=5.2.37-r0
musl=1.2.5-r13
apk_lock=APKS.sha256
EOF
printf 'rootfs: %s\nsha256: %s\nbytes: %s\n' "$output" "$digest" "$size"
