package fd

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type Streams struct {
	Input         io.Reader
	Output, Error io.Writer
}

type reference struct {
	handle Stream
	refs   int

	owned bool
}

func (r *reference) release() error {
	r.refs--
	if r.refs == 0 && r.owned {
		return r.handle.Close()
	}
	return nil
}

type entry struct {
	ref     *reference
	cloexec bool
}

type Table struct {
	mu      sync.Mutex
	entries map[uint64]map[uint32]*entry
}

func New() *Table { return &Table{entries: map[uint64]map[uint32]*entry{}} }

func (t *Table) Attach(pid uint64, s Streams) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.entries[pid] = map[uint32]*entry{
		0: newEntry(&stream{reader: s.Input}, false),
		1: newEntry(&stream{writer: s.Output}, false),
		2: newEntry(&stream{writer: s.Error}, false),
	}
}

func (t *Table) Install(pid uint64, id uint32, handle Stream) error {
	if handle == nil {
		return errno.Invalid
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	table := t.entries[pid]
	if table == nil {
		return errno.NoProcess
	}
	if _, taken := table[id]; taken {
		return errno.Exist
	}
	table[id] = newEntry(handle, true)
	return nil
}

func (t *Table) InstallNext(pid uint64, handle Stream) (uint32, error) {
	if handle == nil {
		return 0, errno.Invalid
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	table := t.entries[pid]
	if table == nil {
		return 0, errno.NoProcess
	}
	for id := uint32(3); id < ^uint32(0); id++ {
		if _, taken := table[id]; !taken {
			table[id] = newEntry(handle, true)
			return id, nil
		}
	}
	return 0, errno.Limit
}

func (t *Table) IsOpen(pid uint64, id uint32) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.entries[pid][id] != nil
}

func (t *Table) Opened(pid uint64) []uint32 {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]uint32, 0, len(t.entries[pid]))
	for id := range t.entries[pid] {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func newEntry(handle Stream, owned bool) *entry {
	return &entry{ref: &reference{handle: handle, refs: 1, owned: owned}}
}

func (t *Table) Detach(pid uint64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	table := t.entries[pid]
	if table == nil {
		return nil
	}
	var closeErr error
	for _, e := range table {
		closeErr = errors.Join(closeErr, e.ref.release())
	}
	delete(t.entries, pid)
	return closeErr
}

func (t *Table) Get(ctx context.Context, pid uint64, id uint32) (Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.entries[pid] == nil {
		return nil, errno.NoProcess
	}
	e := t.entries[pid][id]
	if e == nil {
		return nil, errno.BadHandle
	}
	return e.ref.handle, nil
}

func (t *Table) Dup(ctx context.Context, pid uint64, from, to uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	table := t.entries[pid]
	if table == nil {
		return errno.NoProcess
	}
	source := table[from]
	if source == nil {
		return errno.BadHandle
	}
	if from == to {
		return nil
	}
	if target := table[to]; target != nil {
		closeErr := target.ref.release()
		delete(table, to)
		source.ref.refs++
		table[to] = &entry{ref: source.ref}
		return closeErr
	}
	source.ref.refs++
	table[to] = &entry{ref: source.ref}
	return nil
}

func (t *Table) Close(ctx context.Context, pid uint64, id uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	table := t.entries[pid]
	if table == nil {
		return errno.NoProcess
	}
	e := table[id]
	if e == nil {
		return errno.BadHandle
	}
	delete(table, id)
	return e.ref.release()
}

func (t *Table) Cloexec(pid uint64, id uint32) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.entries[pid][id]
	if e == nil {
		return false, errno.BadHandle
	}
	return e.cloexec, nil
}

func (t *Table) SetCloexec(pid uint64, id uint32, cloexec bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.entries[pid][id]
	if e == nil {
		return errno.BadHandle
	}
	e.cloexec = cloexec
	return nil
}

func (t *Table) CloseCloexec(ctx context.Context, pid uint64) ([]uint32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	table := t.entries[pid]
	if table == nil {
		return nil, errno.NoProcess
	}
	var closed []uint32
	for id, e := range table {
		if !e.cloexec {
			continue
		}
		closed = append(closed, id)
	}
	slices.Sort(closed)
	var closeErr error
	for _, id := range closed {
		e := table[id]
		delete(table, id)
		closeErr = errors.Join(closeErr, e.ref.release())
	}
	return closed, closeErr
}

type terminalStream interface {
	Dimensions() (rows, columns uint16)
}

func (t *Table) Terminal(ctx context.Context, pid uint64, id uint32) (Terminal, error) {
	t.mu.Lock()
	table := t.entries[pid]
	if table == nil {
		t.mu.Unlock()
		return Terminal{}, errno.NoProcess
	}
	entry := table[id]
	if entry == nil {
		t.mu.Unlock()
		return Terminal{}, errno.BadHandle
	}
	handle := entry.ref.handle
	t.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Terminal{}, err
	}
	terminal, ok := handle.(terminalStream)
	if !ok {
		return Terminal{}, nil
	}
	rows, columns := terminal.Dimensions()
	info := Terminal{TTY: true, Rows: rows, Columns: columns}
	if stateful, ok := handle.(StatefulTerminal); ok {
		info.State = stateful.State()
	}
	return info, nil
}

func (t *Table) SetTerminalState(ctx context.Context, pid uint64, id uint32, state TerminalState) error {
	t.mu.Lock()
	table := t.entries[pid]
	if table == nil {
		t.mu.Unlock()
		return errno.NoProcess
	}
	entry := table[id]
	if entry == nil {
		t.mu.Unlock()
		return errno.BadHandle
	}
	handle := entry.ref.handle
	t.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	stateful, ok := handle.(StatefulTerminal)
	if !ok {
		return errno.NotSupported
	}
	return stateful.SetState(state)
}

type stream struct {
	reader io.Reader
	writer io.Writer
}

func (s *stream) Read(p []byte) (int, error) {
	if s.reader == nil {
		return 0, io.EOF
	}
	return s.reader.Read(p)
}
func (s *stream) Write(p []byte) (int, error) {
	if s.writer == nil {
		return 0, io.ErrClosedPipe
	}
	return s.writer.Write(p)
}
func (s *stream) Close() error { return nil }

func (s *stream) ReadContext(ctx context.Context, p []byte) (int, error) {
	if blocking, ok := s.reader.(interface {
		ReadContext(context.Context, []byte) (int, error)
	}); ok {
		return blocking.ReadContext(ctx, p)
	}
	return s.Read(p)
}

func (t *Table) SetTabStops(stops []uint16) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, table := range t.entries {
		for _, entry := range table {
			if entry == nil {
				continue
			}
			if setter, ok := entry.ref.handle.(TabStopSetter); ok {
				return setter.SetTabStops(stops)
			}
		}
	}
	return errno.NotSupported
}

func (t *Table) TabStops() []uint16 {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, table := range t.entries {
		for _, entry := range table {
			if entry == nil {
				continue
			}
			if setter, ok := entry.ref.handle.(TabStopSetter); ok {
				return setter.TabStops()
			}
		}
	}
	return nil
}

var (
	_ Descriptors = (*Table)(nil)

	_ Installer = (*Table)(nil)

	_ TabStopSetter = (*Table)(nil)
)
