// Package model 迁入自原项目 examples/agent/sandbox-contract(A06):
// 新一代 VM 公开模型——VM 拥有持久 Console 与共享文件系统,
// Console.Attach 与 Connect 两种连接生命周期,Run 返回 Result,
// Shutdown 结束一切。行为场景经 Suite 以 VM 工厂注入(A07),
// 模型实现与真实实现共用同一套断言。
package model

import (
	"context"
	"errors"
)

// ErrClosed 是 Shutdown 之后一切入口(Attach/Connect/Run/Screen/
// ReadFile/WriteFile)必须返回的可识别错误;实现以 errors.Is 关联。
var ErrClosed = errors.New("closed")

// ErrBashExited 是 Bash 自身执行 exit 后 Run 返回的稳定错误(P14):
// 会话不会静默重建,调用方通过 State 查询退出码并显式 Restart。
var ErrBashExited = errors.New("bash exited")

// VM 是新模型的根对象:Console 持久存在,Connect 建立独立会话,
// Filesystem 为共享文件系统,Shutdown 幂等结束整机。
type VM interface {
	Console() Console
	Connect(context.Context) (Terminal, error)
	Filesystem() Filesystem
	Shutdown(context.Context) error
}

// Console 是 VM 的持久控制台:断开连接不销毁状态;Screen 读取
// 后台进程持续输出的当前屏幕。
type Console interface {
	Attach(context.Context) (Terminal, error)
	Screen(context.Context) (Screen, error)
}

// Terminal 是一次连接;Console 句柄断开只释放句柄,Connect 句柄
// 断开按实现策略结束其会话。
type Terminal interface {
	Bash() Bash
	Disconnect(context.Context) error
}

// Identified 是 VM/Console/Terminal 的可选寻址能力(R04):同一进程
// 中的对象以稳定字符串标识,不以宿主目录隐式代表 VM。
type Identified interface {
	ID() string
}

// Bash 在该 Terminal 上执行脚本;非零退出码不结束会话。
type Bash interface {
	Run(context.Context, string) (Result, error)
}

// Interrupter 是 Bash 的可选能力(P05):向当前前台命令投递 SIGINT,
// 命令以非零退出码结束,Shell 会话状态保持;与取消等待(调用方不再
// 等待)和 Shutdown(结束整机)语义不同。
type Interrupter interface {
	Interrupt(context.Context) error
}

// BashState 是 Bash 会话的退出状态(P14):Exited 为真后 Run 返回
// ErrBashExited,直到显式 Restart。
type BashState struct {
	Exited   bool
	ExitCode int
}

// StatefulBash 是 Bash 的可选生命周期能力(P14):查询自身退出状态
// 并显式重启。重启是显式操作,不静默替换会话。
type StatefulBash interface {
	State(context.Context) (BashState, error)
	Restart(context.Context) error
}

// 输出来源(P06)。
const (
	OutputForeground = "foreground"
	OutputBackground = "background"
)

// OutputRecord 是带序列边界的输出片段副本;Sequence 单调递增,
// 调用方用上次看到的最大值做增量读取。
type OutputRecord struct {
	Sequence uint64
	Source   string
	Data     []byte
}

// OutputHistory 是 Console 的可选输出历史能力(P06):无连接时前台
// 与后台输出仍按写入顺序记录,来源与序列边界可区分。
type OutputHistory interface {
	Output(context.Context, uint64) ([]OutputRecord, error)
}

// Filesystem 是 VM 级共享文件系统:Console 与全部 Connect 会话
// 看到同一份数据,路径以授权根为界。
type Filesystem interface {
	ReadFile(context.Context, string) ([]byte, error)
	WriteFile(context.Context, string, []byte) error
}

// Result 是一次 Run 的观察结果:输出与退出码。
type Result struct {
	Output   string
	ExitCode int
}

// Position 是屏幕坐标(0 基)。
type Position struct {
	Row    int
	Column int
}

// Screen 是屏幕的原子快照:行内容与光标。
type Screen struct {
	Lines  []string
	Cursor Position
}
