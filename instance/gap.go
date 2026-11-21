package instance

import "github.com/Cosmic-Developers-Union/assistant-sandbox/vm"

// GapReports 返回运行时启动缺口(Q02):每进程首个缺失文件、
// 系统调用或指令,连同自动生成的最小复现源码。
func (v *VM) GapReports() []vm.GapReport { return v.kernel.GapReports() }
