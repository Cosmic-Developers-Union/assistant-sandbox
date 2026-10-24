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

// Bash 在该 Terminal 上执行脚本;非零退出码不结束会话。
type Bash interface {
	Run(context.Context, string) (Result, error)
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
