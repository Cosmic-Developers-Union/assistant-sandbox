package tty

import (
	"context"
	"errors"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/wait"
)

const (
	charINTR = 0x03
	charEOF  = 0x04
	charTSTP = 0x1A
	charQUIT = 0x1C
	charCR   = 0x0D
	charNL   = 0x0A
)

type Flags struct {
	Canonical bool
	Echo      bool
	ISIG      bool
}

type Size struct{ Rows, Cols uint16 }

type Raise func(pgid proc.PID, n signal.Number)

type Terminal struct {
	mu    sync.Mutex
	flags Flags
	size  Size
	fg    proc.PID

	mesg bool

	lines   [][]byte
	pending []byte
	raw     []byte

	outLimit int
	out      []byte

	scr      *screen
	raise    Raise
	closed   bool
	wake     wait.Queue
	watchers []*watchFunc
}

type watchFunc struct{ fn func() }

func New(flags Flags, size Size, outLimit int, raise Raise) (*Terminal, error) {
	if outLimit <= 0 {
		return nil, errno.Invalid
	}
	if size.Rows == 0 {
		size.Rows = 24
	}
	if size.Cols == 0 {
		size.Cols = 80
	}
	return &Terminal{flags: flags, size: size, outLimit: outLimit, raise: raise, fg: 1, mesg: true, scr: newScreen(int(size.Rows), int(size.Cols))}, nil
}

func (t *Terminal) State() fd.TerminalState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return fd.TerminalState{
		Canonical: t.flags.Canonical, Echo: t.flags.Echo, ISIG: t.flags.ISIG, Mesg: t.mesg,
		Rows: t.size.Rows, Columns: t.size.Cols,
	}
}

func (t *Terminal) SetState(state fd.TerminalState) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return errno.BadHandle
	}
	t.flags = Flags{Canonical: state.Canonical, Echo: state.Echo, ISIG: state.ISIG}
	t.mesg = state.Mesg
	if !state.Canonical {
		t.pending = t.pending[:0]
	}
	resized := false
	if state.Rows != 0 && state.Columns != 0 {
		t.size = Size{Rows: state.Rows, Cols: state.Columns}
		resized = true
	}
	if resized {

		t.scr = newScreen(int(t.size.Rows), int(t.size.Cols))
	}
	t.mu.Unlock()
	t.notify()
	if resized && t.raise != nil {

		t.raise(t.Foreground(), signal.WINCH)
	}
	return nil
}

func (t *Terminal) Mesg() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.mesg
}

func (t *Terminal) SetMesg(allowed bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return errno.BadHandle
	}
	t.mesg = allowed
	return nil
}

func (t *Terminal) Input(p []byte) (int, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return 0, errno.BadHandle
	}
	consumed := len(p)
	var echo []byte
	var raises []signal.Number
	for _, b := range p {
		if t.flags.ISIG {
			switch b {
			case charINTR:
				raises = append(raises, signal.INT)
				continue
			case charQUIT:
				raises = append(raises, signal.QUIT)
				continue
			case charTSTP:
				raises = append(raises, signal.TSTP)
				continue
			}
		}
		if t.flags.Canonical {
			switch b {
			case charCR:

				b = charNL
			case charEOF:

				line := make([]byte, len(t.pending))
				copy(line, t.pending)
				t.lines = append(t.lines, line)
				t.pending = t.pending[:0]
				continue
			}
			t.pending = append(t.pending, b)
			if b == charNL {
				line := make([]byte, len(t.pending))
				copy(line, t.pending)
				t.lines = append(t.lines, line)
				t.pending = t.pending[:0]
			}
			if t.flags.Echo {
				echo = append(echo, b)
			}
			continue
		}

		t.raw = append(t.raw, b)
		if t.flags.Echo {
			echo = append(echo, b)
		}
	}
	if len(echo) > 0 && len(t.out)+len(echo) > t.outLimit {

		t.mu.Unlock()
		t.notify()
		return 0, errno.NoSpace
	}
	t.out = append(t.out, echo...)

	t.scr.parse(echo)
	t.mu.Unlock()
	for _, n := range raises {
		if t.raise != nil {
			t.raise(t.foreground(), n)
		}
	}
	t.notify()
	return consumed, nil
}

func (t *Terminal) Read(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return 0, errno.BadHandle
	}
	if len(p) == 0 {
		return 0, nil
	}
	if t.flags.Canonical {
		if len(t.lines) == 0 {
			return 0, errno.WouldBlock
		}
		line := t.lines[0]
		n := copy(p, line)
		if n < len(line) {
			t.lines[0] = line[n:]
		} else {
			t.lines = t.lines[1:]
		}
		return n, nil
	}
	if len(t.raw) == 0 {
		return 0, errno.WouldBlock
	}
	n := copy(p, t.raw)
	t.raw = t.raw[n:]
	return n, nil
}

func (t *Terminal) ReadContext(ctx context.Context, p []byte) (int, error) {
	for {
		n, err := t.Read(p)
		if !errors.Is(err, errno.WouldBlock) {
			return n, err
		}
		if _, err := t.waitReadable(ctx); err != nil {
			return 0, err
		}
	}
}

func (t *Terminal) waitReadable(ctx context.Context) (wait.Reason, error) {
	return t.wake.Wait(ctx, func() (bool, wait.Reason, error) {
		t.mu.Lock()
		defer t.mu.Unlock()
		ready := (t.flags.Canonical && len(t.lines) > 0) || (!t.flags.Canonical && len(t.raw) > 0)
		return ready, wait.Data, nil
	})
}

func (t *Terminal) Write(p []byte) (int, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return 0, errno.BadHandle
	}
	if len(t.out)+len(p) > t.outLimit {
		t.mu.Unlock()
		return 0, errno.NoSpace
	}
	t.out = append(t.out, p...)
	t.scr.parse(p)
	t.mu.Unlock()
	t.notify()
	return len(p), nil
}

func (t *Terminal) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return errno.BadHandle
	}
	t.closed = true
	t.mu.Unlock()
	t.wake.Close()
	return nil
}

func (t *Terminal) Drain() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.out
	t.out = nil
	t.mu.Unlock()
	t.notify()
	t.mu.Lock()
	return out
}

func (t *Terminal) Flags() Flags {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.flags
}

func (t *Terminal) SetFlags(flags Flags) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return errno.BadHandle
	}
	t.flags = flags
	if !flags.Canonical {
		t.pending = t.pending[:0]
	}
	t.mu.Unlock()
	t.notify()
	t.mu.Lock()
	return nil
}

func (t *Terminal) Size() Size {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.size
}

func (t *Terminal) Dimensions() (rows, columns uint16) {
	size := t.Size()
	return size.Rows, size.Cols
}

func (t *Terminal) TabStops() []uint16 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.scr.TabStops()
}

func (t *Terminal) SetTabStops(stops []uint16) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return errno.BadHandle
	}
	return t.scr.SetTabStops(stops)
}

func (t *Terminal) ScreenView() ([]string, int, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.scr.View()
}

func (t *Terminal) SetSize(size Size) error {
	if size.Rows == 0 || size.Cols == 0 {
		return errno.Invalid
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return errno.BadHandle
	}
	t.size = size

	t.scr = newScreen(int(size.Rows), int(size.Cols))
	t.mu.Unlock()
	t.notify()
	if t.raise != nil {
		t.raise(t.Foreground(), signal.WINCH)
	}
	return nil
}

func (t *Terminal) Foreground() proc.PID {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.fg
}

func (t *Terminal) SetForeground(pgid proc.PID) error {
	if pgid == 0 {
		return errno.Invalid
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return errno.BadHandle
	}
	t.fg = pgid
	return nil
}

func (t *Terminal) foreground() proc.PID {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.fg
}

func (t *Terminal) Ready() poll.Readiness {
	t.mu.Lock()
	defer t.mu.Unlock()
	var bits poll.Readiness
	if (t.flags.Canonical && len(t.lines) > 0) || (!t.flags.Canonical && len(t.raw) > 0) {
		bits |= poll.Readable
	}
	if len(t.out) < t.outLimit {
		bits |= poll.Writable
	}
	return bits
}

func (t *Terminal) OnChange(fn func()) (cancel func()) {
	w := &watchFunc{fn: fn}
	t.mu.Lock()
	t.watchers = append(t.watchers, w)
	t.mu.Unlock()
	return func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		for i, registered := range t.watchers {
			if registered == w {
				t.watchers = append(t.watchers[:i], t.watchers[i+1:]...)
				return
			}
		}
	}
}

func (t *Terminal) notify() {
	t.wake.WakeAll(wait.Data)
	t.mu.Lock()
	watchers := make([]*watchFunc, len(t.watchers))
	copy(watchers, t.watchers)
	t.mu.Unlock()
	for _, w := range watchers {
		w.fn()
	}
}
