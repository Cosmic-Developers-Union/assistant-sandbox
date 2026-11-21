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
