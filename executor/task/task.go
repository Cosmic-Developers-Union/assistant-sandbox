// Package task 实现任务切换(D10/D11):同一进程内的多个任务共享一个
// 执行核心,各自持有完整 CPU 状态——程序计数器、32 个整数寄存器
// (含 TLS 指针寄存器)、32 个浮点寄存器与 FCSR。切换时保存当前任务
// 的完整状态并恢复目标任务的状态,整数、浮点与 TLS 寄存器互不污染。
//
// 时间片有界:每执行固定条数指令后切换到下一个任务,纯 CPU 死循环
// 不会独占核心,其他任务继续前进。
package task

import (
	"context"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// Task 是一个可切换的执行任务:持有自己的地址空间与完整 CPU 状态。
type Task struct {
	memory cpu.Memory
	state  cpu.Machine
	active bool
}

// New 创建任务:指定其地址空间与初始机器状态。
func New(memory cpu.Memory, state cpu.Machine) *Task {
	return &Task{memory: memory, state: state}
}

// Memory 返回任务的地址空间。
func (t *Task) Memory() cpu.Memory { return t.memory }

// State 返回任务保存的机器状态副本。
func (t *Task) State() cpu.Machine {
	out := cpu.Machine{PC: t.state.PC, FCSR: t.state.FCSR}
	out.Registers = make([]uint64, len(t.state.Registers))
	copy(out.Registers, t.state.Registers)
	out.Floats = make([]uint64, len(t.state.Floats))
	copy(out.Floats, t.state.Floats)
	return out
}

// Set 是任务集合:共享一个执行核心,按时间片轮流推进各任务。
type Set struct {
	core  cpu.Port
	tasks []*Task
	index int
	slice uint64
	used  uint64
}

// NewSet 创建任务集合:core 是共享的执行核心,tasks 至少一个,
// slice 是时间片指令数(0 表示不切换)。
func NewSet(core cpu.Port, tasks []*Task, slice uint64) (*Set, error) {
	if core == nil || len(tasks) == 0 {
		return nil, errno.Wrap(errno.Invalid, "任务集合需要执行核心与至少一个任务")
	}
	for _, task := range tasks {
		if task == nil || task.memory == nil {
			return nil, errno.Wrap(errno.Invalid, "任务必须有地址空间")
		}
	}
	return &Set{core: core, tasks: tasks, slice: slice}, nil
}

// Len 返回任务数。
func (s *Set) Len() int { return len(s.tasks) }

// Current 返回当前任务。
func (s *Set) Current() *Task { return s.tasks[s.index] }

// Tick 记一次指令执行;时间片用尽时返回 true,调用方应当切换任务。
func (s *Set) Tick() bool {
	if s.slice == 0 {
		return false
	}
	s.used++
	if s.used < s.slice {
		return false
	}
	s.used = 0
	return true
}

// Switch 保存当前任务的完整状态并切换到下一个任务:核心换绑目标
// 地址空间,机器状态整体恢复。返回是否发生了切换(单任务集合不切换)。
func (s *Set) Switch(ctx context.Context) (bool, error) {
	if len(s.tasks) < 2 {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	current := s.tasks[s.index]
	state, err := s.core.State(ctx)
	if err != nil {
		return false, err
	}
	current.state = state
	current.active = true
	s.index = (s.index + 1) % len(s.tasks)
	next := s.tasks[s.index]
	attacher, ok := s.core.(cpu.Attacher)
	if !ok {
		return false, errno.Wrap(errno.NotSupported, "执行核心不支持换绑地址空间")
	}
	attacher.Attach(next.memory)
	if err := s.core.Reset(ctx, next.state); err != nil {
		return false, err
	}
	next.active = true
	return true, nil
}

// Save 保存当前任务的完整状态(不切换)。
func (s *Set) Save(ctx context.Context) error {
	state, err := s.core.State(ctx)
	if err != nil {
		return err
	}
	s.tasks[s.index].state = state
	return nil
}
