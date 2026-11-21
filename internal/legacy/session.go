package legacy

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/protocol"
)

type loopBudgetKey struct{}

type Session struct {
	runtime            *Runtime
	dir                string
	gate               chan struct{}
	input              *io.PipeWriter
	output             *io.PipeReader
	scanner            *bufio.Scanner
	stop               func()
	done               chan struct{}
	wire               *outputBudget
	id                 uint64
	initialized, reset bool
	lifetime           context.Context
	cancelLifetime     context.CancelFunc
}

func (r *Runtime) Bind(dir string) (*Session, error) {
	full, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := validateCacheOutsideWorkspace(full, r.cfg.CompilationCacheDir); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, fmt.Errorf("WASI runtime 已关闭")
	}
	if len(r.sessions) >= r.cfg.MaxSessions {
		return nil, fmt.Errorf("持续 Bash 会话达到 %d 个上限", r.cfg.MaxSessions)
	}
	lifetime, cancel := context.WithCancel(r.base)
	session := &Session{runtime: r, dir: full, gate: make(chan struct{}, 1), lifetime: lifetime, cancelLifetime: cancel}
	r.sessions[session] = true
	return session, nil
}

func (s *Session) boot() {
	ctx, cancel := context.WithCancel(s.lifetime)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s.input = inW
	s.output = outR
	s.done = make(chan struct{})
	s.wire = &outputBudget{remaining: 16 << 20, cancel: cancel}
	ctx = context.WithValue(ctx, loopBudgetKey{}, s.wire)
	s.scanner = bufio.NewScanner(outR)
	s.scanner.Buffer(make([]byte, 4096), 16<<20)
	unblock := context.AfterFunc(ctx, func() { _, _ = inR.Close(), outW.Close() })
	s.stop = sync.OnceFunc(func() {
		cancel()
		_, _ = inW.Close(), inR.Close()
		_, _ = outR.Close(), outW.Close()
		<-s.done
		unblock()
	})
	go func() {
		defer close(s.done)
		defer func() { _, _ = inR.Close(), outW.Close() }()
		_, err := s.runtime.execute(ctx, s.dir, s.runtime.shell, []string{"assistant-bash"}, nil, inR, outW, io.Discard, cancel)
		outW.CloseWithError(err)
	}()
	s.initialized = true
}

func (s *Session) Execute(ctx context.Context, dir, command string, stdout, stderr io.Writer) (Result, error) {
	if command == "" || len(command) > 64<<10 {
		return Result{}, fmt.Errorf("Bash 脚本不能为空或超过 64 KiB")
	}
	full, err := filepath.Abs(dir)
	if err != nil || full != s.dir {
		return Result{}, fmt.Errorf("Bash 会话不能切换宿主工作区")
	}
	ctx, cancel := context.WithTimeout(ctx, s.runtime.cfg.Timeout)
	defer cancel()
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	select {
	case s.runtime.slots <- struct{}{}:
		defer func() { <-s.runtime.slots }()
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	s.runtime.mu.Lock()
	alive := s.runtime.sessions[s] && !s.runtime.closed
	s.runtime.mu.Unlock()
	if !alive {
		return Result{}, fmt.Errorf("Bash 会话已关闭")
	}
	if !s.initialized {
		s.boot()
	}
	reset := s.reset
	s.reset = false
	s.wire.mu.Lock()
	s.wire.remaining = 16 << 20
	s.wire.exceeded = false
	s.wire.mu.Unlock()
	interrupted := context.AfterFunc(ctx, s.stop)
	defer interrupted()
	message, messages, err := messageSnapshot(ctx, s.runtime.cfg.MessageSource)
	if err != nil {
		return Result{}, err
	}
	s.id++
	data, err := json.Marshal(protocol.Request{ID: s.id, Command: command, Message: message, Messages: messages, OutputLimit: s.runtime.cfg.OutputBytes})
	if err != nil {
		return Result{}, err
	}
	_, err = s.input.Write(append(data, '\n'))
	var response protocol.Response
	if err == nil {
		if s.scanner.Scan() {
			err = json.Unmarshal(s.scanner.Bytes(), &response, json.RejectUnknownMembers(true))
		} else {
			err = errors.Join(io.ErrUnexpectedEOF, s.scanner.Err())
		}
	}
	if err != nil || ctx.Err() != nil {
		s.stop()
		s.initialized = false
		s.reset = true
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return Result{}, fmt.Errorf("Bash loop 中断，状态将在下次调用重建: %w", err)
	}
	if response.ID != s.id {
		s.stop()
		s.initialized = false
		s.reset = true
		return Result{}, fmt.Errorf("Bash loop 返回了错误的命令编号")
	}
	if response.Closed || response.Overflow {
		s.stop()
		s.initialized = false
		s.reset = true
	}
	if _, err := io.WriteString(stdout, response.Stdout); err != nil {
		return Result{}, err
	}
	if reset {
		if _, err := io.WriteString(stderr, "Bash loop 已重建：之前的变量、函数和目录状态已清除。\n"); err != nil {
			return Result{}, err
		}
	}
	if _, err := io.WriteString(stderr, response.Stderr); err != nil {
		return Result{}, err
	}
	if response.Overflow {
		return Result{}, fmt.Errorf("沙箱输出超过 %d 字节预算", s.runtime.cfg.OutputBytes)
	}
	return Result{ExitCode: response.Code, Closed: response.Closed}, nil
}

func (s *Session) Close() error {
	s.cancelLifetime()
	s.gate <- struct{}{}
	defer func() { <-s.gate }()
	if s.initialized {
		s.stop()
		s.initialized = false
	}
	s.runtime.mu.Lock()
	delete(s.runtime.sessions, s)
	for dir, session := range s.runtime.defaults {
		if session == s {
			delete(s.runtime.defaults, dir)
		}
	}
	s.runtime.mu.Unlock()
	return nil
}
