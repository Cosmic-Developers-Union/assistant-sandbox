package resources

import (
	"context"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type Limits struct {
	Memory      uint64
	Work        uint64
	Descriptors uint64
	Output      uint64
	Files       uint64
}

type usage [KindCount]uint64

const KindCount = 5

func (k Kind) index() (int, bool) {
	position := int(k)
	if position < 0 || position >= KindCount {
		return 0, false
	}
	return position, true
}

func (l Limits) limitOf(kind Kind) (uint64, bool) {
	switch kind {
	case KindMemory:
		return l.Memory, true
	case KindWork:
		return l.Work, true
	case KindDescriptors:
		return l.Descriptors, true
	case KindOutput:
		return l.Output, true
	case KindFiles:
		return l.Files, true
	default:
		return 0, false
	}
}

type tier struct {
	limits Limits
	used   usage
}

type Meters struct {
	mu       sync.Mutex
	session  tier
	procs    map[uint64]*tier
	defaults Limits
}

func NewSession(limits Limits, defaults Limits) (*Meters, error) {
	if limits.Memory == 0 || limits.Work == 0 {
		return nil, errno.Invalid
	}
	return &Meters{session: tier{limits: limits}, procs: map[uint64]*tier{}, defaults: defaults}, nil
}

func (m *Meters) Spawn(pid uint64, limits Limits) error {

	limits = m.resolveLimits(limits)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.withinSession(limits); err != nil {
		return err
	}
	m.procs[pid] = &tier{limits: limits}
	return nil
}

func (m *Meters) resolveLimits(limits Limits) Limits {
	for kind := Kind(0); int(kind) < KindCount; kind++ {
		current, _ := limits.limitOf(kind)
		if current != 0 {
			continue
		}
		if fallback, ok := m.defaults.limitOf(kind); ok && fallback != 0 {
			limits = limits.withLimit(kind, fallback)
			continue
		}
		if fallback, ok := m.session.limits.limitOf(kind); ok {
			limits = limits.withLimit(kind, fallback)
		}
	}
	return limits
}

func (l Limits) withLimit(kind Kind, value uint64) Limits {
	switch kind {
	case KindMemory:
		l.Memory = value
	case KindWork:
		l.Work = value
	case KindDescriptors:
		l.Descriptors = value
	case KindOutput:
		l.Output = value
	case KindFiles:
		l.Files = value
	}
	return l
}

func (m *Meters) withinSession(limits Limits) error {
	for kind := Kind(0); int(kind) < KindCount; kind++ {
		sessionLimit, ok := m.session.limits.limitOf(kind)
		if !ok || sessionLimit == 0 {
			continue
		}
		processLimit, _ := limits.limitOf(kind)
		if processLimit > sessionLimit {
			return errno.Limit
		}
	}
	return nil
}

func (m *Meters) View(ctx context.Context, pid uint64) (View, error) {
	if err := ctx.Err(); err != nil {
		return View{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.procs[pid]
	if p == nil {
		return View{}, errno.NoProcess
	}
	return viewOf(p.limits, p.used), nil
}

func (m *Meters) SessionView(ctx context.Context) (View, error) {
	if err := ctx.Err(); err != nil {
		return View{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return viewOf(m.session.limits, m.session.used), nil
}

func viewOf(limits Limits, used usage) View {
	return View{
		MemoryLimit:     limits.Memory,
		MemoryUsed:      used[KindMemory],
		WorkLimit:       limits.Work,
		WorkUsed:        used[KindWork],
		DescriptorLimit: limits.Descriptors,
		DescriptorUsed:  used[KindDescriptors],
		OutputLimit:     limits.Output,
		OutputUsed:      used[KindOutput],
		FileLimit:       limits.Files,
		FileUsed:        used[KindFiles],
	}
}

func (m *Meters) Charge(ctx context.Context, pid uint64, work uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.procs[pid]
	if p == nil {
		return errno.NoProcess
	}
	return m.chargeLocked(p, KindWork, work)
}

func (m *Meters) ChargeKind(ctx context.Context, pid uint64, kind Kind, amount uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := kind.index(); !ok {
		return errno.Wrap(errno.Invalid, "未知资源类别")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.procs[pid]
	if p == nil {
		return errno.NoProcess
	}
	return m.chargeLocked(p, kind, amount)
}

func (m *Meters) chargeLocked(p *tier, kind Kind, amount uint64) error {
	position, ok := kind.index()
	if !ok {
		return errno.Wrap(errno.Invalid, "未知资源类别")
	}

	if limit, _ := p.limits.limitOf(kind); limit > 0 && p.used[position]+amount > limit {
		return errno.Limit
	}
	if limit, _ := m.session.limits.limitOf(kind); limit > 0 && m.session.used[position]+amount > limit {
		return errno.Limit
	}
	p.used[position] += amount
	m.session.used[position] += amount
	return nil
}

func (m *Meters) ReleaseKind(ctx context.Context, pid uint64, kind Kind, amount uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	position, ok := kind.index()
	if !ok {
		return errno.Wrap(errno.Invalid, "未知资源类别")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.procs[pid]
	if p == nil {
		return errno.NoProcess
	}
	if p.used[position] < amount || m.session.used[position] < amount {
		return errno.Wrap(errno.Invalid, "归还量超过已用量")
	}
	p.used[position] -= amount
	m.session.used[position] -= amount
	return nil
}

func (m *Meters) Reserve(ctx context.Context, pid uint64, memory uint64) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.procs[pid]
	if p == nil {
		return nil, errno.NoProcess
	}
	if err := m.chargeLocked(p, KindMemory, memory); err != nil {
		return nil, err
	}
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		position, _ := KindMemory.index()
		if target := m.procs[pid]; target != nil && target.used[position] >= memory {
			target.used[position] -= memory
			m.session.used[position] -= memory
		}
	}, nil
}

func (m *Meters) Discard(pid uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.procs[pid]
	if p == nil {
		return
	}
	for _, kind := range []Kind{KindMemory, KindDescriptors, KindFiles} {
		position, ok := kind.index()
		if !ok {
			continue
		}
		if m.session.used[position] >= p.used[position] {
			m.session.used[position] -= p.used[position]
		}
		p.used[position] = 0
	}
	delete(m.procs, pid)
}
