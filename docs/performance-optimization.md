# 性能优化任务(T11)

首个测得瓶颈来自 T08 `BenchmarkWriteSyscall`:每次 64 字节 write 分配
816 B/3 allocs,主要成本是每次调用新建读缓冲。

## 已提交:I/O 写缓冲复用

- 任务:小写入(≤4 KiB)从 `sync.Pool` 复用读缓冲,大写入仍按需分配。
- 实现:`abi/linux/common/syscalls.go` `writeBufferPool`/`writeBuffer`。
- 前:`1582 ns/op,816 B/op,3 allocs/op`。
- 后:`1085 ns/op,753 B/op,2 allocs/op`。
- 语义回归:`go test ./abi/linux/common/ -count=1`。
- 基准:`go test ./abi/linux/common/ -run '^$' -bench BenchmarkWrite -benchmem`。

## 已提交:内存访问保护检查快速路径

- 任务:单映射内的访存用二分查找定位区间,只有跨相邻映射时才
  做全区间覆盖扫描;保留跨边界语义。
- 实现:`vm/memory/table.go` `checkLocked` 快速路径。
- 前:Q03 Python 3.12 最小启动约 77s。
- 后:同一测试约 49s。
- 语义回归:`go test ./vm/memory/ -count=1`、Python Q03 测试。

## 后续独立任务(分别提交)

- CPU:`getpid` 分派已无分配;待有更大负载时再测量,禁止无基准改动。
- 内存:VM 创建/进程克隆的分配曲线;先加基准再优化。
- I/O:大写入与 socket 路径分配;先加基准再优化。

每项优化必须给出前后基准与语义回归,并在独立提交中完成。
