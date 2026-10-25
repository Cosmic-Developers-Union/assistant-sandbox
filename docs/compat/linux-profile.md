# Linux 兼容配置 linux-rv64gc-1(A03)

状态:现行。依据 [VM 架构决策](../adr/vm-architecture.md)(A02)。

本配置是可执行的兼容边界,不是无限兼容承诺:

- **未列出的行为一律不支持**:未登记系统调用返回 ENOSYS,已登记但不
  支持的子集返回 ENOTSUP;绝不以无操作成功伪装完成。
- 每项能力映射到 TODO.md 的任务与测试;扩大本配置必须同时给出用例,
  并同步 `abi/linux/common/registry.go` 与
  [一致性测试](../../abi/linux/common/profile_test.go)。
- 系统调用三张表与登记表的逐名一致性由
  `TestProfileSyscallTablesMatchRegistry` 在 CI 中锁定。

## 1. 指令集(RV64GC + Zicsr/Zifencei 用户子集)

| 类别 | 内容 | 状态 | 任务/测试 |
| --- | --- | --- | --- |
| RV64I 基础整数 | 立即数/寄存器运算、分支、跳转、LOAD/STORE、LUI/AUIPC、ECALL/EBREAK | 已交付(逐条差分审计中) | D02;`executor/cpu/riscv64` 全部向量 |
| M 乘除 | MUL/MULH*/DIV/DIVU/REM/REMU 含除零与有符号最小值 | 已交付 | D03 |
| A 原子 | AMO 九种操作 × W/D、LR/SC 保留语义 | 已交付 | D05/D06 |
| C 压缩 | RV64C 全部编码;C.FLD/FSD/FLDSP/FSDSP 待浮点压缩落地 | 已交付(浮点压缩除外) | D04 |
| F/D 浮点 | 单双精度算术、转换、比较、fcsr、五种舍入、全部标志 | 已交付 | D07/D08 |
| Zicsr/Zifencei 用户子集 | cycle/instret 只读 CSR、fcsr、fence/fence.i | 已交付(time CSR 除外) | D09 |
| 特权指令、S 模式 CSR | — | 不支持(非法指令陷入) | A02:S 组不适用 |

## 2. 系统调用

编号与名称一一对应由 G01 的 `TestRegistryMatchesAdapterMapping` 锁定;
适配器认识但未实现的 34 个编号(epoll_create1、inotify_*、memfd_create、
ppoll、pselect6、timerfd_*、signalfd4、clone3、openat2、faccessat2、
ptrace、chroot、mount、umount2、sync、acct、reboot、kexec_load、
syslog、pivot_root、perf_event_open、pidfd_open、pidfd_getfd、
close_range、fadvise64、fallocate、userfaultfd、accept4)固定登记为
ENOSYS 清单,归 L/I/H 组后续任务。

### 已交付语义(65 项)

| 调用 | 语义来源(任务/测试) |
| --- | --- |
| exit / exit_group | H08;`vm/proc` 生命周期测试 |
| clone | 仅 flags==SIGCHLD 即 fork 形态(线程标志 ENOTSUP,K 组);H04;`TestRealELFForkParentChildReturnValues` |
| execve | H06/H07;`TestRealELFExecve*` |
| waitid | H09;`TestKernelWaitChild*`、`TestWaitid*` |
| getpid / gettid / getppid | G 组身份查询 |
| openat / close / read / write | I03/I06;`TestOpenFlag*`、`TestReadWriteShort*` |
| readv / writev | I07;`TestReadvWritevScatterGather` |
| lseek / getdents64 | I09;`TestGetdents64Walks*` |
| fstat / newfstatat / statx | I08;`TestFstat*`、`TestStatx*` |
| statfs / fstatfs | I19;`TestStatfs*` |
| mkdirat / unlinkat / renameat / renameat2 | I10;`TestDirectoryOperations*` |
| symlinkat / linkat / readlinkat | I11;`TestLinkSymlinkLifecycle*` |
| ftruncate / fsync / fdatasync / utimensat | I12;`TestUtimensatAndFtruncate*` |
| faccessat | G 组;`TestFaccessat*` |
| fcntl / dup / dup3 | F_DUPFD、F_DUPFD_CLOEXEC、F_GETFL、F_SETFL;I05;`TestDupAndFcntlFlags*` |
| pipe / pipe2 | vm/pipe;`TestKernel*` 管道用例 |
| eventfd2 | L09;`TestEventfd2ValidatesAndDelegates` |
| brk / mmap / munmap / mprotect / mremap / madvise | mmap 仅匿名私有映射,文件映射 ENOTSUP;E03/E06/E07/E11/E12;`TestMmap*`、`TestBrk*` |
| rt_sigaction / rt_sigprocmask / rt_sigreturn / sigaltstack | J02/J04;`TestRtSig*`、`TestSigaltstack*` |
| kill / tgkill | J05;`TestKillSignalDispatchSemantics` |
| clock_gettime / clock_getres / gettimeofday | L02;`TestClockGettime` |
| clock_nanosleep | L03/J09;`TestClockNanosleep*` |
| getrandom | L04;`TestGetrandomValidatesFlags` |
| uname / sysinfo | G09;`TestUnameAndSysinfo*` |
| getrlimit / setrlimit / prlimit64 | G10;`TestGetrlimit*`、`TestPrlimit64*` |
| sched_getaffinity / sched_setaffinity / getcpu | L12;`TestAffinityAndTopology*` |
| getcwd | G 组;`vm` 内核用例 |
| getuid / geteuid / getgid / getegid | G 组单身份查询(O01 前固定客体身份) |
| getpgid / getsid / setpgid / setsid | H11;`TestKernelProcessGroupsAndSessions`、`TestSessionProbeErrnosMatchLinuxSemantics` |

### 明确 ENOTSUP(31 项:在册、语义未交付)

| 调用 | 承接任务 |
| --- | --- |
| socket / bind / listen / accept / connect / socketpair / sendto / recvfrom / shutdown / setsockopt / getsockopt / getsockname / getpeername | M02–M08 |
| getitimer / setitimer | 未纳入配置(需要时立任务) |
| getpriority / setpriority | 未纳入配置(需要时立任务) |
| mlock / munlock / mlockall / munlockall / mincore | E15(映射/物理页预算) |
| umask | I13(权限/umask/所有权) |
| setuid / setgid / setreuid / setregid / setresuid / setresgid / setfsuid / setfsgid | O01(客体身份) |

### 声明语义(4 项:返回值即配置,不是缺陷)

| 调用 | 声明 | 复核任务 |
| --- | --- | --- |
| sched_yield | 顺序解释器无抢占,让出为成功空操作 | H02 落地后复核 |
| times / getrusage | 结构体全零写入 | T03 资源指标 |
| getgroups | 无补充组:返回 0 | 单身份模型声明(O01 后复核) |

## 3. 文件、路径与地址空间

- 文件类型:常规文件、目录、符号链接、硬链接(memfs/overlay/mountfs);
  设备节点仅由 devfs 提供;FIFO 与 socket 创建不在配置内(I15/M 组前
  mkfifo/socket 相关 ENOTSUP 或 ENOSYS)。
- 路径解析:绝对路径与 AT_FDCWD;**dirfd 相对解析明确 ENOTSUP**(I02);
  `.`/`..`、符号链接(含 ELOOP)由 memfs 语义提供;不能逃逸授权根(N11)。
- open 标志矩阵、短读短写、目录游标、截断/时间戳按 I03/I06/I09/I12。
- 地址空间:匿名私有 mmap(E03)、固定地址替换(E04)、munmap 拆分(E05)、
  mprotect 含 W^X 拒绝(E06)、brk(E07)、mremap/madvise 子集(E11/E12)、
  fork 克隆(E10)。**文件映射 ENOTSUP**(E08/E09);栈保护页(E13)未配置。
- 挂载:同 VM 统一挂载表,只读层与写时复制(I16);跨挂载 rename EXDEV。
- 持久化:memfs 为内存介质;崩溃恢复(I17)与文件锁(I14)不在本版配置。

## 4. 进程、信号与时间

- 进程:Begin/Run/Finish 生命周期、fork、execve、waitid(P_PID/P_ALL +
  WEXITED/WNOHANG)、孤儿收养与僵尸回收(H10);进程上限与会话计量。
- 进程组与会话(H11):子进程默认继承父进程的进程组与会话;setpgid 仅能
  调整调用方自身或其子进程,跨会话与组长目标报 EPERM,目标进程或进程组
  不存在报 ESRCH;已是组长时 setsid 报 EPERM;组信号按组内成员投递。
  getpgid/getsid 对负 pid 与不存在进程同样报 ESRCH(与 Linux 内核一致;
  glibc 包装层另行返回 EINVAL)。
- 不支持:vfork(H12)、clone 线程标志(K 组)、ptrace、procfs 查询(N06/N07)。
- 信号:标准信号投递、掩码/处置/备用栈、信号帧与 rt_sigreturn、
  kill/tgkill/进程组播、同步故障转换、STOP/CONT/KILL、SIGCHLD 通知、
  EINTR/SA_RESTART(睡眠与等待)。
- 时间:实时时钟(realtime)、单调时钟(monotonic)与测试虚拟时钟分离(L01);
  单调读数只增不减,相对等待期限一律以单调时钟计量——系统时间调整既
  不改变相对期限,也不让单调时间回退;绝对期限按调用方指定的时钟计算。
  客体可见时间由注入的时钟来源提供(CPU 数固定为配置值,缺省 1)。

## 5. 设备与网络

- 设备:`/dev/null`、`/dev/zero`、`/dev/random`、`/dev/urandom`(N05)、
  eventfd(L09);PTY(I/O 术语层)不在本版系统调用配置。
- 网络:整组 ENOTSUP(M01–M13),DNS/TLS 场景未配置。

## 6. 差分基线

同一客体程序应在本 VM 与参考 Linux(A09 差分执行器)执行并对比返回值、
errno、输出与副作用;PID/时间等非固定值归一化。本配置中的每一项
"已交付语义"都应出现在差分矩阵;差异即缺陷,不因文档声明而豁免。
