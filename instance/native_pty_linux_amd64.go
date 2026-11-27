//go:build linux && amd64

package instance

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/tty"
	"golang.org/x/sys/unix"
)

const (
	nativeSessionStart = "\x1b]8;;assistant-start:"
	nativeSessionEnd   = "\x1b]8;;assistant-end:"
	nativeSessionReady = "\x1b]8;;assistant-ready\x07"
)

var errNativeBashExited = errors.New("native bash exited")

// NativeSession 是客体 Bash 与真实 PTY 组成的持久会话。
type NativeSession struct {
	vm      *NativeVM
	master  *os.File
	process *NativeProcess
	screen  *tty.Terminal
	done    chan struct{}

	runMu         sync.Mutex
	mu            sync.Mutex
	closed        bool
	echo          bool
	pendingOutput []byte
	rows          uint16
	cols          uint16
}

// NativeSessionSpec 描述原生 PTY 会话的客体 shell 与终端参数。
type NativeSessionSpec struct {
	Program string
	Args    []string
	Env     map[string]string
	Rows    uint16
	Cols    uint16
	Echo    bool
}

// StartSession 启动供 model.VM 使用的安静 Bash PTY 会话。
func (v *NativeVM) StartSession(ctx context.Context, environment map[string]string) (*NativeSession, error) {
	return v.StartSessionSpec(ctx, NativeSessionSpec{
		Program: "/bin/bash", Args: []string{"--noprofile", "--norc", "--noediting", "-i"},
		Env: environment, Rows: 24, Cols: 100,
	})
}

// StartSessionSpec 启动持久客体 shell PTY 会话。
func (v *NativeVM) StartSessionSpec(ctx context.Context, spec NativeSessionSpec) (*NativeSession, error) {
	if spec.Program == "" || !strings.HasPrefix(spec.Program, "/") {
		return nil, fmt.Errorf("native session program must be absolute: %w", errno.Invalid)
	}
	rows, cols := spec.Rows, spec.Cols
	if rows == 0 {
		rows = 24
	}
	if cols == 0 {
		cols = 80
	}
	master, slave, err := openNativePTY(rows, cols, spec.Echo)
	if err != nil {
		return nil, err
	}
	env := map[string]string{
		"PATH": "/usr/local/bin:/usr/bin:/bin", "HOME": "/work",
		"LANG": "C.UTF-8", "LC_ALL": "C.UTF-8", "TERM": "xterm-256color",
		"PS1": "", "PS2": "", "USER": "sandbox", "LOGNAME": "sandbox",
	}
	maps.Copy(env, spec.Env)
	if v.egress != nil {
		maps.Copy(env, v.egress.environment())
		env["ASSISTANT_SANDBOX_EGRESS_PROXY"] = "1"
	}
	process, err := v.Exec(ctx, NativeExecSpec{
		Program: spec.Program, Args: spec.Args,
		Dir: "/work", Env: env, Stdin: slave, Stdout: slave, Stderr: slave,
	})
	_ = slave.Close()
	if err != nil {
		_ = master.Close()
		return nil, err
	}
	screen, err := tty.New(tty.Flags{}, tty.Size{Rows: rows, Cols: cols}, 64<<20, nil)
	if err != nil {
		_ = process.Cancel()
		_ = master.Close()
		return nil, err
	}
	session := &NativeSession{
		vm: v, master: master, process: process, screen: screen,
		done: make(chan struct{}), rows: rows, cols: cols, echo: spec.Echo,
	}
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		_ = session.Close(context.Background())
		return nil, errno.Cancelled
	}
	if v.sessions == nil {
		v.sessions = map[*NativeSession]struct{}{}
	}
	v.sessions[session] = struct{}{}
	v.mu.Unlock()
	go session.pumpOutput()
	if err := session.initialize(ctx); err != nil {
		_ = session.Close(context.Background())
		return nil, err
	}
	return session, nil
}

func openNativePTY(rows, cols uint16, echo bool) (*os.File, *os.File, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open PTY master: %w", err)
	}
	unlock := int32(0)
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, master.Fd(), unix.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); errno != 0 { //nolint:gosec // ioctl requires the kernel ABI pointer
		_ = master.Close()
		return nil, nil, fmt.Errorf("unlock PTY: %w", errno)
	}
	var number uint32
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, master.Fd(), unix.TIOCGPTN, uintptr(unsafe.Pointer(&number))); errno != 0 { //nolint:gosec // ioctl requires the kernel ABI pointer
		_ = master.Close()
		return nil, nil, fmt.Errorf("get PTY number: %w", errno)
	}
	slave, err := os.OpenFile(filepath.Join("/dev/pts", strconv.FormatUint(uint64(number), 10)), os.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = master.Close()
		return nil, nil, fmt.Errorf("open PTY slave: %w", err)
	}
	if err := setNativePTYSize(master, rows, cols); err != nil {
		_ = slave.Close()
		_ = master.Close()
		return nil, nil, err
	}
	termios, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		_ = slave.Close()
		_ = master.Close()
		return nil, nil, fmt.Errorf("read PTY termios: %w", err)
	}
	if echo {
		termios.Lflag |= unix.ECHO
	} else {
		termios.Lflag &^= unix.ECHO | unix.ECHONL
	}
	if err := unix.IoctlSetTermios(int(slave.Fd()), unix.TCSETS, termios); err != nil {
		_ = slave.Close()
		_ = master.Close()
		return nil, nil, fmt.Errorf("disable PTY echo: %w", err)
	}
	if err := unix.SetNonblock(int(master.Fd()), true); err != nil {
		_ = slave.Close()
		_ = master.Close()
		return nil, nil, fmt.Errorf("set PTY master nonblocking: %w", err)
	}
	return master, slave, nil
}

func setNativePTYSize(master *os.File, rows, cols uint16) error {
	size := unix.Winsize{Row: rows, Col: cols}
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, master.Fd(), unix.TIOCSWINSZ, uintptr(unsafe.Pointer(&size))); errno != 0 { //nolint:gosec // ioctl requires the kernel ABI pointer
		return fmt.Errorf("resize PTY: %w", errno)
	}
	return nil
}

func (s *NativeSession) pumpOutput() {
	defer close(s.done)
	defer func() { _ = s.screen.Close() }()
	buffer := make([]byte, 8192)
	for {
		read, err := s.master.Read(buffer)
		if read > 0 {
			_, _ = s.screen.Write(buffer[:read])
		}
		if err != nil {
			if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			return
		}
	}
}

func (s *NativeSession) initialize(ctx context.Context) error {
	echo := "stty -echo"
	if s.echo {
		echo = "stty echo"
	}
	command := echo + "; printf '\\033[2J\\033[H\\033]8;;assistant-ready\\007'\n"
	if err := s.Write([]byte(command)); err != nil {
		return err
	}
	_, err := s.WaitOutput(ctx, nativeSessionReady)
	return err
}

// ID 返回宿主可见的会话 init 进程号。
func (s *NativeSession) ID() int { return s.process.ID() }

// Write 向客体 PTY 输入字节。
func (s *NativeSession) Write(data []byte) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return errno.Cancelled
	}
	return writeNativePTY(s.master, data)
}

func writeNativePTY(file *os.File, data []byte) error {
	for len(data) > 0 {
		written, err := file.Write(data)
		if written > 0 {
			data = data[written:]
		}
		if err != nil {
			if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			return err
		}
		if written == 0 {
			return errors.New("PTY write made no progress")
		}
	}
	return nil
}

// DrainContext 读取客体输出增量。
func (s *NativeSession) DrainContext(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	if len(s.pendingOutput) > 0 {
		pending := s.pendingOutput
		s.pendingOutput = nil
		s.mu.Unlock()
		return pending, nil
	}
	s.mu.Unlock()
	return s.screen.DrainContext(ctx)
}

func (s *NativeSession) keepPendingOutput(data []byte) {
	if len(data) == 0 {
		return
	}
	s.mu.Lock()
	s.pendingOutput = append(s.pendingOutput, data...)
	s.mu.Unlock()
}

// WaitOutput 累积输出直到出现 marker 或上下文结束。
func (s *NativeSession) WaitOutput(ctx context.Context, marker string) (string, error) {
	var output bytes.Buffer
	for {
		chunk, err := s.DrainContext(ctx)
		if err != nil {
			return output.String(), err
		}
		output.Write(chunk)
		if output.Len() > 4<<20 {
			return output.String(), fmt.Errorf("native session output exceeds 4 MiB: %w", errno.NoSpace)
		}
		if bytes.Contains(output.Bytes(), []byte(marker)) {
			markerAt := bytes.Index(output.Bytes(), []byte(marker))
			end := markerAt + len(marker)
			s.keepPendingOutput(output.Bytes()[end:])
			return output.String()[:end], nil
		}
	}
}

func (s *NativeSession) exited() bool {
	select {
	case <-s.process.done:
		return true
	default:
		return false
	}
}

// runScript 在当前 Bash 中 source 临时脚本，并用不可见 OSC marker 隔离
// 前台输出与退出码；脚本中的 cd/export/function 因此保留在会话状态里。
func (s *NativeSession) runScript(ctx context.Context, script string) (string, int, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	if strings.IndexByte(script, 0) >= 0 {
		return "", 0, fmt.Errorf("native session script contains NUL: %w", errno.Invalid)
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.exited() {
		return "", 0, errNativeBashExited
	}
	for {
		chunk := s.screen.TryDrain()
		if len(chunk) == 0 {
			break
		}
	}
	file, err := os.CreateTemp(filepath.Join(s.vm.root, "work"), ".assistant-run-*")
	if err != nil {
		return "", 0, fmt.Errorf("create native session script: %w", err)
	}
	name := file.Name()
	defer func() { _ = os.Remove(name) }()
	if !strings.HasSuffix(script, "\n") {
		script += "\n"
	}
	if _, err := file.WriteString(script); err != nil {
		_ = file.Close()
		return "", 0, fmt.Errorf("write native session script: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", 0, fmt.Errorf("close native session script: %w", err)
	}
	token := rand.Text()
	guestPath := "/work/" + filepath.Base(name)
	start := []byte(nativeSessionStart + token + "\x07")
	endPrefix := []byte(nativeSessionEnd + token + ":")
	command := fmt.Sprintf("printf '\\033]8;;assistant-start:%s\\007'; . '%s'; __assistant_status=$?; disown -a >/dev/null 2>&1; printf '\\033]8;;assistant-end:%s:%%d\\007' \"$__assistant_status\"\n", token, guestPath, token)
	if err := s.Write([]byte(command)); err != nil {
		return "", 0, err
	}
	var output bytes.Buffer
	for {
		chunk, err := s.DrainContext(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				_ = s.Interrupt()
			}
			if s.exited() {
				return output.String(), 0, errNativeBashExited
			}
			return output.String(), 0, err
		}
		output.Write(chunk)
		if output.Len() > 4<<20 {
			return output.String(), 0, fmt.Errorf("native session output exceeds 4 MiB: %w", errno.NoSpace)
		}
		data := output.Bytes()
		startAt := bytes.Index(data, start)
		if startAt < 0 {
			continue
		}
		endAt := bytes.Index(data[startAt+len(start):], endPrefix)
		if endAt < 0 {
			continue
		}
		endAt += startAt + len(start)
		statusAt := endAt + len(endPrefix)
		statusEnd := bytes.IndexByte(data[statusAt:], 0x07)
		if statusEnd < 0 {
			continue
		}
		code, err := strconv.Atoi(string(data[statusAt : statusAt+statusEnd]))
		if err != nil || code < 0 || code > 255 {
			return output.String(), 0, fmt.Errorf("invalid native session status %q: %w", data[statusAt:statusAt+statusEnd], errno.Invalid)
		}
		return string(data[startAt+len(start) : endAt]), code, nil
	}
}

// ScreenView 返回当前终端屏幕。
func (s *NativeSession) ScreenView() ([]string, int, int) {
	return s.screen.ScreenView()
}

// Resize 更新客体 PTY 窗口尺寸。
func (s *NativeSession) Resize(rows, cols uint16) error {
	if rows == 0 || cols == 0 {
		return errno.Invalid
	}
	if err := setNativePTYSize(s.master, rows, cols); err != nil {
		return err
	}
	s.mu.Lock()
	s.rows, s.cols = rows, cols
	s.mu.Unlock()
	return s.screen.SetSize(tty.Size{Rows: rows, Cols: cols})
}

// Interrupt 通过 PTY 控制字符向前台任务投递 SIGINT。
func (s *NativeSession) Interrupt() error {
	return s.Write([]byte{0x03})
}

// Wait 等待 Bash 会话退出。
func (s *NativeSession) Wait(ctx context.Context) (NativeExit, error) {
	return s.process.Wait(ctx)
}

// Close 关闭 PTY 与会话进程，幂等。
func (s *NativeSession) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	_ = s.process.Cancel()
	select {
	case <-s.process.done:
	case <-ctx.Done():
	}
	_ = s.master.Close()
	select {
	case <-s.done:
	case <-time.After(time.Second):
	}
	_ = s.screen.Close()
	if s.vm != nil {
		s.vm.mu.Lock()
		delete(s.vm.sessions, s)
		s.vm.mu.Unlock()
	}
	return ctx.Err()
}
