#!/usr/bin/env bash
# Q01:从固定源码构建 riscv64-linux-musl CPython 3.12.16。
# 依赖:zig 0.17.0、qemu-riscv64-static、curl、tar、make。
# 输出:$WORK/stage/usr/local/bin/python3.12 与 $WORK/manifest.txt。
set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
WORK=${1:-"$HERE/python312-build"}
ZIG=${ZIG:-zig}
QEMU=${QEMU_RISCV64:-qemu-riscv64-static}
SRC_URL="https://www.python.org/ftp/python/3.12.16/Python-3.12.16.tgz"
SRC_SHA="710c3e75dac8b2f258d9cd0f76e5c4c2e9175fac9e249c98901552f7cf33b7dd"

mkdir -p "$WORK"
cd "$WORK"

if [ ! -f Python-3.12.16.tgz ]; then
	curl -L --fail --max-time 600 -o Python-3.12.16.tgz "$SRC_URL"
fi
echo "$SRC_SHA  Python-3.12.16.tgz" | sha256sum -c -

rm -rf src buildpy sysroot stage
mkdir -p src buildpy sysroot/lib stage
tar -xzf Python-3.12.16.tgz -C src --strip-components=1

# 用固定 APK 中的 3.12 解释器作为 build Python,经 qemu 运行。
tar -xf "$HERE/python3-3.12.16-r0.apk" -C buildpy
cp "$HERE/../lib/ld-musl-riscv64.so.1" sysroot/lib/
cp "$HERE/../lib/ld-musl-riscv64.so.1" sysroot/lib/libc.musl-riscv64.so.1
mkdir -p sysroot/usr/local/lib
cp buildpy/usr/lib/libpython3.12.so.1.0 sysroot/usr/local/lib/
cat > build-python <<EOF
#!/bin/sh
QEMU_LD_PREFIX="$WORK/sysroot" PYTHONHOME="$WORK/buildpy/usr" exec "$QEMU" -L "$WORK/sysroot" "$WORK/buildpy/usr/bin/python3.12" "\$@"
EOF
chmod +x build-python
./build-python --version

cd src
CC="$ZIG cc -target riscv64-linux-musl" \
AR="$ZIG ar" \
RANLIB="$ZIG ranlib" \
CFLAGS="-O2" \
LDFLAGS="-static" \
ac_cv_file__dev_ptmx=yes \
ac_cv_file__dev_ptc=no \
ac_cv_buggy_getaddrinfo=no \
./configure \
	--host=riscv64-linux-musl \
	--build=x86_64-pc-linux-gnu \
	--with-build-python="$WORK/build-python" \
	--disable-shared \
	--without-ensurepip \
	--without-doc-strings \
	--disable-test-modules \
	--disable-ipv6
# 核心构建不依赖 zlib/ssl/sqlite 等可选原生依赖;它们由 Q07 单独打包。
for module in _bz2 _lzma zlib readline _ctypes _curses _curses_panel _dbm _gdbm _ssl _sqlite3 _uuid _tkinter binascii _crypt nis; do
	sed -i -E "s/^($module) /# \\1 /" Modules/Setup.stdlib
done
# 固定构建时间,避免 __DATE__/__TIME__ 进入产物。
sed -i 's/#define DATE __DATE__/#define DATE "Jan 01 1970"/; s/#define TIME __TIME__/#define TIME "00:00:00"/' Modules/getbuildinfo.c
SOURCE_DATE_EPOCH=0 make -j"$(nproc)"
make install DESTDIR="$WORK/stage"

PYTHON="$WORK/stage/usr/local/bin/python3.12"
QEMU_LD_PREFIX="$WORK/sysroot" PYTHONHOME="$WORK/stage/usr/local" \
	"$QEMU" -L "$WORK/sysroot" "$PYTHON" --version
QEMU_LD_PREFIX="$WORK/sysroot" PYTHONHOME="$WORK/stage/usr/local" \
	"$QEMU" -L "$WORK/sysroot" "$PYTHON" -S -c 'print(2+3)'

BINARY_SHA=$(sha256sum "$PYTHON" | awk '{print $1}')
STDLIB_SHA=$(find "$WORK/stage/usr/local/lib/python3.12" -type f -print0 | sort -z | xargs -0 sha256sum | sha256sum | awk '{print $1}')
STDLIB_FILES=$(find "$WORK/stage/usr/local/lib/python3.12" -type f | wc -l)
{
	echo "source=$SRC_URL"
	echo "source_sha256=$SRC_SHA"
	echo "zig=$($ZIG version)"
	echo "target=riscv64-linux-musl"
	echo "configure=--disable-shared --without-ensurepip --disable-ipv6"
	echo "python_sha256=$BINARY_SHA"
	echo "stdlib_files=$STDLIB_FILES"
	echo "stdlib_sha256=$STDLIB_SHA"
	echo "python=$PYTHON"
	echo "smoke=--version + -S -c print(2+3)"
} > "$WORK/manifest.txt"
cat "$WORK/manifest.txt"
