// Package pty 实现客体 PTY 主从设备(P08):单个终端对由
// /dev/ptmx 主端与 /dev/pts/0 从端共享行规程、窗口大小与 termios。
package pty

import (
	"context"
	"errors"
	"io"
	stdfs "io/fs"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/tty"
)

type pair struct {
	terminal *tty.Terminal
	number   uint32
	close    sync.Once
}

func (p *pair) closeTerminal() {
	p.close.Do(func() { _ = p.terminal.Close() })
}

// Manager 分配 PTY 对。当前兼容配置提供一个从端(/dev/pts/0)。
type Manager struct {
	mu   sync.Mutex
	pair *pair
}

func NewManager() *Manager { return &Manager{} }

func (m *Manager) ensure() *pair {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pair == nil {
		terminal, _ := tty.New(tty.Flags{Canonical: true, Echo: true, ISIG: true}, tty.Size{Rows: 24, Cols: 80}, 1<<20, nil)
		m.pair = &pair{terminal: terminal}
	}
	return m.pair
}

func (m *Manager) OpenMaster() (*Master, error) { return &Master{pair: m.ensure()}, nil }

func (m *Manager) OpenSlave(number uint32) (*Slave, error) {
	if number != 0 {
		return nil, errno.Wrap(errno.NotFound, "没有该 PTY 从端")
	}
	return &Slave{pair: m.ensure()}, nil
}

func (m *Manager) SlaveFS() vmfs.System { return &slaveFS{manager: m} }

type Master struct {
	pair *pair
	mu   sync.Mutex
	rest []byte
}

func (m *Master) Read(buffer []byte) (int, error) {
	m.mu.Lock()
	if len(m.rest) > 0 {
		n := copy(buffer, m.rest)
		m.rest = m.rest[n:]
		m.mu.Unlock()
		return n, nil
	}
	m.mu.Unlock()
	data, err := m.pair.terminal.DrainContext(context.Background())
	if err != nil {
		if errors.Is(err, errno.BadHandle) {
			return 0, io.EOF
		}
		return 0, err
	}
	n := copy(buffer, data)
	if n < len(data) {
		m.mu.Lock()
		m.rest = append(m.rest, data[n:]...)
		m.mu.Unlock()
	}
	return n, nil
}

func (m *Master) Write(buffer []byte) (int, error) { return m.pair.terminal.Input(buffer) }

func (m *Master) Close() error {
	m.pair.closeTerminal()
	return nil
}

func (m *Master) Dimensions() (uint16, uint16) { return m.pair.terminal.Dimensions() }

func (m *Master) State() fd.TerminalState { return m.pair.terminal.State() }

func (m *Master) SetState(state fd.TerminalState) error { return m.pair.terminal.SetState(state) }

func (m *Master) PTYNumber() uint32 { return m.pair.number }

type Slave struct{ pair *pair }

func (s *Slave) Read(buffer []byte) (int, error) {
	return s.pair.terminal.ReadContext(context.Background(), buffer)
}

func (s *Slave) Write(buffer []byte) (int, error) { return s.pair.terminal.Write(buffer) }

func (s *Slave) Close() error {
	s.pair.closeTerminal()
	return nil
}

func (s *Slave) Dimensions() (uint16, uint16) { return s.pair.terminal.Dimensions() }

func (s *Slave) State() fd.TerminalState { return s.pair.terminal.State() }

func (s *Slave) SetState(state fd.TerminalState) error { return s.pair.terminal.SetState(state) }

type slaveFile struct {
	slave *Slave
}

func (f *slaveFile) Read(buffer []byte) (int, error)  { return f.slave.Read(buffer) }
func (f *slaveFile) Write(buffer []byte) (int, error) { return f.slave.Write(buffer) }
func (f *slaveFile) Close() error                     { return f.slave.Close() }
func (f *slaveFile) Seek(int64, int) (int64, error) {
	return 0, errno.Wrap(errno.NotSupported, "PTY 从端不可定位")
}
func (f *slaveFile) Stat() (stdfs.FileInfo, error)         { return fileInfo{name: "0"}, nil }
func (f *slaveFile) Dimensions() (uint16, uint16)          { return f.slave.Dimensions() }
func (f *slaveFile) State() fd.TerminalState               { return f.slave.State() }
func (f *slaveFile) SetState(state fd.TerminalState) error { return f.slave.SetState(state) }

type slaveFS struct{ manager *Manager }

func (f *slaveFS) Open(ctx context.Context, name string, flags uint32, mode stdfs.FileMode) (vmfs.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimPrefix(path.Clean(name), "/") != "0" {
		return nil, errno.Wrap(errno.NotFound, "没有该 PTY 从端")
	}
	slave, err := f.manager.OpenSlave(0)
	if err != nil {
		return nil, err
	}
	return &slaveFile{slave: slave}, nil
}

func (f *slaveFS) Stat(ctx context.Context, name string) (stdfs.FileInfo, error) {
	return f.info(ctx, name)
}

func (f *slaveFS) Lstat(ctx context.Context, name string) (stdfs.FileInfo, error) {
	return f.info(ctx, name)
}

func (f *slaveFS) info(ctx context.Context, name string) (stdfs.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	clean := strings.TrimPrefix(path.Clean(name), "/")
	switch clean {
	case "", ".":
		return fileInfo{name: "pts", dir: true}, nil
	case "0":
		return fileInfo{name: "0"}, nil
	default:
		return nil, errno.Wrap(errno.NotFound, "没有该 PTY 从端")
	}
}

func (f *slaveFS) ReadDir(ctx context.Context, name string) ([]stdfs.DirEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimPrefix(path.Clean(name), "/") != "" && strings.TrimPrefix(path.Clean(name), "/") != "." {
		return nil, errno.Wrap(errno.NotDirectory, "不是目录")
	}
	return []stdfs.DirEntry{dirEntry{name: "0"}}, nil
}

func (f *slaveFS) Mkdir(context.Context, string, stdfs.FileMode) error {
	return errno.Wrap(errno.ReadOnlyFS, "PTY 目录只读")
}
func (f *slaveFS) Remove(context.Context, string) error {
	return errno.Wrap(errno.ReadOnlyFS, "PTY 目录只读")
}
func (f *slaveFS) Rename(context.Context, string, string) error {
	return errno.Wrap(errno.ReadOnlyFS, "PTY 目录只读")
}
func (f *slaveFS) Link(context.Context, string, string) error {
	return errno.Wrap(errno.ReadOnlyFS, "PTY 目录只读")
}
func (f *slaveFS) Symlink(context.Context, string, string) error {
	return errno.Wrap(errno.ReadOnlyFS, "PTY 目录只读")
}
func (f *slaveFS) Readlink(context.Context, string) (string, error) {
	return "", errno.Wrap(errno.Invalid, "不是符号链接")
}

type fileInfo struct {
	name string
	dir  bool
}

func (i fileInfo) Name() string { return i.name }
func (i fileInfo) Size() int64  { return 0 }
func (i fileInfo) Mode() stdfs.FileMode {
	if i.dir {
		return stdfs.ModeDir | 0o755
	}
	return stdfs.ModeDevice | stdfs.ModeCharDevice | 0o620
}
func (i fileInfo) ModTime() time.Time { return time.Time{} }
func (i fileInfo) IsDir() bool        { return i.dir }
func (i fileInfo) Sys() any           { return nil }

type dirEntry struct{ name string }

func (d dirEntry) Name() string                  { return d.name }
func (d dirEntry) IsDir() bool                   { return false }
func (d dirEntry) Type() stdfs.FileMode          { return stdfs.ModeDevice | stdfs.ModeCharDevice }
func (d dirEntry) Info() (stdfs.FileInfo, error) { return fileInfo{name: d.name}, nil }
