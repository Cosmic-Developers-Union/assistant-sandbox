# Guest musl loader

`ld-musl-riscv64.so.1` 由 musl 1.2.6 上游源码构建,仅增加一处
`fpathconf(3)` 描述符校验:

- 源码:`musl-1.2.6.tar.gz`,
  sha256 `d585fd3b613c66151fc3249e8ed44f77020cb5e6c1e635a616d3f9f82460512a`
- 构建摘要:2026-11-24,clang 21 + ld.lld
- 产物 sha256:
  `23ea7ef0c0fb58d350b0a847c75de7529a68c240af87c4fdd4a327444801b0fd`

补丁文件:[`fpathconf-ebadf.patch`](fpathconf-ebadf.patch)。

上游 musl 的 `fpathconf` 对已知 `name` 直接返回常量表值,不触碰
描述符;CPython `test.test_os.TestInvalidFD.test_fpathconf` 要求关闭
描述符返回 `EBADF`。该补丁只改变 guest libc 的这一处行为,不涉及
内核 ABI。

构建命令(在 musl 源码根目录):

```sh
CC="clang --target=riscv64-linux-musl -march=rv64gc -mabi=lp64d -fuse-ld=lld" \
AR=llvm-ar RANLIB=llvm-ranlib \
./configure --host=riscv64-linux-musl

# configure 找不到交叉 libgcc;指向 guest GCC 工具链中的 libgcc。
sed -i 's|^LIBCC = .*|LIBCC = /path/to/libgcc.a /path/to/libgcc_eh.a|' config.mak
make -j"$(nproc)"
llvm-strip --strip-all lib/libc.so
```

`libgcc.a` 与 `libgcc_eh.a` 取自
`testdata/gcc14-cxx-riscv64-toolchain.tar.gz`。不要使用 Zig 自带
compiler-rt:其弱 `memcpy`/`strlen` 定义会让 musl 的公开符号变成
局部隐藏符号,动态 CPython 无法重定位。
