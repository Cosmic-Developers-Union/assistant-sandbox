package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/language"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

const LanguageID = "bash"

// ErrExited 是 Bash 自身执行 exit 后的稳定错误:会话不会静默重建,
// 调用方必须先读取退出状态并显式 Restart(P14)。
var ErrExited = errors.New("Bash 会话已退出")

// ExitState 是 Bash 会话的退出状态(P14)。
type ExitState struct {
	Exited   bool
	ExitCode int
}

type slotState struct {
	Dir string            `json:"dir"`
	Env map[string]string `json:"env,omitempty"`
}

type Slot struct {
	mu      sync.Mutex
	kernel  *vm.Kernel
	parent  proc.Proc
	session *session
	sink    *mutableSink
	dir     string
	env     []string
	baseDir string
	baseEnv []string
	control *sessionControl

	// exitMu 保护退出状态:Step 串行执行,但 State/Restart 可被其他
	// 句柄并发查询(P14)。
	exitMu   sync.Mutex
	exited   bool
	exitCode int

	// outputs 是 Console 级输出历史(P06):无连接时也累计前后台输出。
	outputs *outputLog

	// run 是执行权令牌(P04):同一槽位的 Step 串行执行,排队者在
	// 等待令牌时可被上下文取消,取消的请求不会在之后被执行。
	run chan struct{}

	// interrupt 是当前前台 Run 的取消与中断标记(P05):Interrupt 先
	// 向前台进程投递 SIGINT,再取消解释器避免继续执行同一条命令行
	// 的后续语句;退出码固定为 130。
	interruptMu     sync.Mutex
	interruptCancel context.CancelFunc
	interrupted     bool

	closed bool
}

func NewSlot(kernel *vm.Kernel, parent proc.Proc, dir string, environment []string) (*Slot, error) {
	return NewSlotWithOutput(kernel, parent, dir, environment, nil)
}

// NewSlotWithOutput 建立槽位并把持续输出(命令返回后仍在写入的后台
// 进程输出)同时送往 output;output 为 nil 时只保留逐步骤的捕获。
func NewSlotWithOutput(kernel *vm.Kernel, parent proc.Proc, dir string, environment []string, output io.Writer) (*Slot, error) {
	if kernel == nil {
		return nil, errno.Wrap(errno.Invalid, "缺少内核")
	}

	sink := &mutableSink{}
	sink.setPersistent(output)
	outputs := newOutputLog(4096)
	sink.setObserver(outputs.record)
	session, err := newSessionWithKernel(kernel, parent, dir, environment, nil, sink, sink)
	if err != nil {
		return nil, err
	}
	return &Slot{
		kernel:  kernel,
		parent:  parent,
		session: session,
		sink:    sink,
		dir:     dir,
		env:     append([]string(nil), environment...),
		baseDir: dir,
		baseEnv: append([]string(nil), environment...),
		run:     make(chan struct{}, 1),
		control: session.control,
		outputs: outputs,
	}, nil
}

// Output 返回序号大于 sequence 的前后台输出片段(P06);sequence=0
// 从头读取。输出历史归 Console 槽位所有,不依赖连接句柄。
func (s *Slot) Output(sequence uint64) []OutputChunk {
	return s.outputs.since(sequence)
}

// Interrupt 向前台命令进程投递 SIGINT(P05):命令按默认处置终止,
// 解释器停止当前命令行剩余语句,Shell 槽位、目录与变量保持,不重建
// 会话。没有前台命令时仍取消当前 Run(状态 130)。
func (s *Slot) Interrupt(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.control == nil {
		return errno.Wrap(errno.NotSupported, "槽位未接入前台命令控制")
	}
	signalErr := s.control.interrupt(ctx)
	s.interruptMu.Lock()
	if s.interruptCancel != nil {
		s.interrupted = true
		s.interruptCancel()
	}
	s.interruptMu.Unlock()
	return signalErr
}

// acquire 取得执行权;排队期间上下文取消直接返回,不执行命令(P04)。
func (s *Slot) acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.run <- struct{}{}:
		return nil
	}
}

// release 归还执行权。
func (s *Slot) release() {
	<-s.run
}

func (s *Slot) Language() string { return LanguageID }

const stepBaseCost = 1

func (s *Slot) Step(ctx context.Context, source []byte) (language.Output, error) {
	if err := ctx.Err(); err != nil {
		return language.Output{}, err
	}
	if err := s.acquire(ctx); err != nil {
		return language.Output{}, err
	}
	defer s.release()
	if err := ctx.Err(); err != nil {
		return language.Output{}, err
	}

	if err := s.charge(ctx, len(source)); err != nil {
		return language.Output{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return language.Output{}, errno.Wrap(errno.Cancelled, "槽位已关闭")
	}
	if s.session == nil {
		return language.Output{}, errno.Wrap(errno.Busy, "槽位已结束")
	}
	s.exitMu.Lock()
	exited := s.exited
	s.exitMu.Unlock()
	if exited {
		return language.Output{}, ErrExited
	}

	var buffer bytes.Buffer
	s.sink.setTarget(&buffer)
	defer s.sink.setTarget(nil)
	tree, err := parse(string(source))
	if err != nil {

		return language.Output{Data: []byte(err.Error()), Status: 2, Done: false}, nil
	}
	// 顶层后台语句独立执行:后台任务不随前台命令的上下文取消而结束
	// (与 Linux 的后台作业一致)。
	foreground, background := splitBackground(tree)
	if background != nil {
		s.runBackground(ctx, background)
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.interruptMu.Lock()
	s.interruptCancel = cancel
	s.interrupted = false
	s.interruptMu.Unlock()
	defer func() {
		s.interruptMu.Lock()
		s.interruptCancel = nil
		s.interruptMu.Unlock()
		cancel()
	}()
	runErr := s.session.runner.Run(proc.WithContext(runCtx, s.parent), foreground)
	status, done := exitStatus(runErr)
	s.interruptMu.Lock()
	interrupted := s.interrupted
	s.interruptMu.Unlock()
	if interrupted {
		status = 130
	}
	if s.session.runner.Exited() && !interrupted && ctx.Err() == nil {
		s.exitMu.Lock()
		s.exited = true
		s.exitCode = status
		s.exitMu.Unlock()
	}
	s.dir = s.session.runner.Dir

	_ = s.kernel.RunDueJobs(ctx)
	return language.Output{Data: append([]byte(nil), buffer.Bytes()...), Status: status, Done: done}, nil
}

// splitBackground 把顶层语句按后台标记拆成前台与后台两部分:后台
// 语句由独立的解释器执行,输出仍写入同一个汇聚点。
func splitBackground(tree *syntax.File) (foreground, background *syntax.File) {
	if tree == nil {
		return nil, nil
	}
	front := &syntax.File{Name: tree.Name}
	back := &syntax.File{Name: tree.Name}
	for _, statement := range tree.Stmts {
		if statement.Background {
			back.Stmts = append(back.Stmts, statement)
			continue
		}
		front.Stmts = append(front.Stmts, statement)
	}
	if len(back.Stmts) == 0 {
		return tree, nil
	}
	return front, back
}

// runBackground 在独立解释器里执行后台语句:上下文与前台命令解耦,
// 前台返回后后台继续运行。
func (s *Slot) runBackground(ctx context.Context, tree *syntax.File) {
	output := s.sink.persistentWriter()
	runner, err := NewRunner(s.kernel, s.parent, s.dir, s.env, nil, output, output)
	if err != nil {
		return
	}
	go func() {
		_ = runner.Run(proc.WithContext(context.WithoutCancel(ctx), s.parent), tree)
	}()
}

func exitStatus(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	if status, ok := errors.AsType[interp.ExitStatus](err); ok {
		return int(status), false
	}
	return 1, false
}

func (s *Slot) charge(ctx context.Context, size int) error {
	meter, err := s.parent.Resources.Meter()
	if err != nil {
		if errors.Is(err, errno.NotImplemented) {
			return nil
		}
		return err
	}
	cost := uint64(size) + stepBaseCost
	return meter.ChargeKind(ctx, uint64(s.parent.ID), resources.KindWork, cost)
}

// State 查询 Bash 会话是否已执行 exit(P14)。退出不会自动重建。
func (s *Slot) State(ctx context.Context) (ExitState, error) {
	if err := ctx.Err(); err != nil {
		return ExitState{}, err
	}
	s.exitMu.Lock()
	defer s.exitMu.Unlock()
	return ExitState{Exited: s.exited, ExitCode: s.exitCode}, nil
}

// Restart 显式重建 Bash 解释器(P14):新会话使用槽位创建时的工作
// 目录与环境,原会话的目录、变量与函数不继承;调用方必须观察到
// State.Exited 后自行决定重启,系统不静默重建。
func (s *Slot) Restart(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer s.release()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errno.Wrap(errno.Cancelled, "槽位已关闭")
	}
	session, err := newSessionWithKernel(s.kernel, s.parent, s.baseDir, s.baseEnv, nil, s.sink, s.sink)
	if err != nil {
		return err
	}
	s.session = session
	s.control = session.control
	s.dir = s.baseDir
	s.env = append([]string(nil), s.baseEnv...)
	s.exitMu.Lock()
	s.exited = false
	s.exitCode = 0
	s.exitMu.Unlock()
	return nil
}

func (s *Slot) Encode(ctx context.Context) (language.State, error) {
	if err := ctx.Err(); err != nil {
		return language.State{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return language.State{}, errno.Wrap(errno.Cancelled, "槽位已关闭")
	}
	payload, err := json.Marshal(slotState{Dir: s.dir, Env: environMap(s.env)})
	if err != nil {
		return language.State{}, errno.Wrap(err, "编码 Bash 槽位状态")
	}
	encoded, err := language.State{Language: LanguageID, Payload: payload}.Encode()
	if err != nil {
		return language.State{}, err
	}
	return language.Decode(encoded)
}

func (s *Slot) Restore(ctx context.Context, state language.State) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errno.Wrap(errno.Cancelled, "槽位已关闭")
	}
	if state.Language != LanguageID {
		return errno.Wrap(errno.Invalid, "状态语言与槽位不符")
	}
	var restored slotState
	if err := json.Unmarshal(state.Payload, &restored); err != nil {
		return errno.Wrap(err, "解析 Bash 槽位状态")
	}
	if restored.Dir != "" {
		s.dir = restored.Dir
		if s.session != nil {
			s.session.runner.Dir = restored.Dir
		}
	}
	if restored.Env != nil {
		s.env = environList(restored.Env)
	}
	return nil
}

func (s *Slot) Close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.session != nil {
		s.session.close()
		s.session = nil
	}
	return nil
}

func environMap(environ []string) map[string]string {
	out := make(map[string]string, len(environ))
	for _, entry := range environ {
		for index := 0; index < len(entry); index++ {
			if entry[index] == '=' {
				out[entry[:index]] = entry[index+1:]
				break
			}
		}
	}
	return out
}

func environList(environ map[string]string) []string {
	names := make([]string, 0, len(environ))
	for name := range environ {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, name+"="+environ[name])
	}
	return out
}
