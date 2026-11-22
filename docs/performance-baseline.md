# 性能与管理响应基线(T08/T10)

测量环境:Linux amd64,AMD Ryzen 7 5700G,Go 工具链由 go.mod 固定。
基准命令:

```
go test ./abi/linux/common/ -run '^$' -bench 'BenchmarkGetpid|BenchmarkWrite' -benchmem -count=5
```

最近一次记录:getpid 67.17 ns/op、0 B/op、0 allocs/op;64 字节 write
1582 ns/op、40.47 MB/s、816 B/op、3 allocs/op。CI `benchmark` 任务保存
`benchmark.txt` 工件,用于同环境前后对比。

发布门限(超过即失败,不以延长超时通过):

- 10k 次 getpid 分派 < 1s(`TestSyscallPerformanceEnvelope`)。
- 创建 VM、100 次状态快照、关闭 VM 各 < 2s
  (`TestManagementResponseEnvelope`)。

容差:门限相对当前测量保留约一个数量级余量,适配共享 CI 主机抖动;
若测量环境变化,先更新本文件与 T08 记录,再调整门限。

## Python/Node 场景基线(T09)

以下数据由 `go test ./internal/guest/ -run 'TestPythonNodeSceneBaseline|TestRuntimeMemorySnapshot' -count=1 -v`
在同一台 Linux amd64 主机、真实 RISC-V 客体运行时上测得;启动时间包含
固定运行时资产写入 VM 的时间,不是宿主解释器启动时间。

| 场景 | 启动 | 文件/并发工作量 | 进程内存 |
| --- | ---: | ---: | ---: |
| Python 3.12.16 | 240 ms | 1 MiB 读写 + 4 线程 × 2,000 次加锁:37.8 s | 15.4 MiB |
| Node.js 22.23.5(js 模式 `--jitless`) | 432 ms | 1 MiB 读写 + 20,000 次算术:73.2 s | 77.0 MiB |

内存值来自运行中的 `instance.Status` 进程预算(`MemoryUsed`),不是宿主
`getrusage`;两者都执行客体 ELF。并发服务基线由 Q11/Q19/Q22/Q31 的
asyncio、Node HTTP/TLS 与双运行时重连用例固定,依赖导入基线由 Q13/Q14/
Q27/Q29/Q30 的固定运行时用例固定。基线仅用于同配置前后比较,不作为
跨主机绝对性能承诺。
