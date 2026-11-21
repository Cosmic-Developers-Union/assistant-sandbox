# CPython 3.12.16 客体资产

`python3-3.12.16-r0.apk` 为 Alpine 3.22 riscv64 的官方软件包,
sha256 `38e902b011e1218292cbc25d3259c98007e437d61159dcffb4504cfdb1b2d487`。
测试时按固定路径解包到客体,不使用宿主 Python:

- `usr/bin/python3.12` → `/usr/bin/python3.12`
- `usr/lib/libpython3.12.so.1.0` → `/usr/lib/libpython3.12.so.1.0`
- `usr/lib/python3.12/` → `/usr/lib/python3.12/`
- musl loader 复用 `testdata/lib/ld-musl-riscv64.so.1`。

Q01 仍需把 APK 提取替换为固定源码构建流水线,并记录完整 SBOM。
