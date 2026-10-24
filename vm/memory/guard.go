package memory

import (
	"context"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type Violation struct {
	Start, End uint64

	Requested Prot

	Existing Region

	Fault Fault
}

type Reporter func(context.Context, Violation)

type Guard struct {
	inner    *Table
	reporter Reporter
}

func NewGuard(inner *Table, reporter Reporter) *Guard {
	if inner == nil {
		inner = New(0)
	}
	return &Guard{inner: inner, reporter: reporter}
}

func (g *Guard) report(ctx context.Context, violation Violation) {
	if g.reporter == nil {
		return
	}
	g.reporter(ctx, violation)
}

// Inner 暴露守卫包装的底层地址空间表(内核 fork 续接执行所需)。
func (g *Guard) Inner() *Table { return g.inner }

// Clone 复制守卫下的地址空间(fork 语义),返回覆盖克隆的新守卫;
// 违例上报器按调用方重新装配。
func (g *Guard) Clone(reporter Reporter) (*Guard, error) {
	cloned, err := g.inner.Clone()
	if err != nil {
		return nil, err
	}
	return &Guard{inner: cloned, reporter: reporter}, nil
}

func (g *Guard) Map(ctx context.Context, region Region) error {
	err := g.inner.Map(ctx, region)
	if err == nil {
		return nil
	}
	g.report(ctx, Violation{
		Start: region.Start, End: region.End,
		Requested: region.Prot,
		Fault:     faultOfErr(err, region),
	})
	return err
}

func (g *Guard) Unmap(ctx context.Context, start, end uint64) error {
	err := g.inner.Unmap(ctx, start, end)
	if err == nil {
		return nil
	}
	g.report(ctx, Violation{Start: start, End: end, Fault: faultOfErr(err, Region{Start: start, End: end})})
	return err
}

func (g *Guard) Protect(ctx context.Context, start, end uint64, prot Prot) error {
	err := g.inner.Protect(ctx, start, end, prot)
	if err == nil {
		return nil
	}
	violation := Violation{Start: start, End: end, Requested: prot}
	if existing, ok := g.existingAt(ctx, start); ok {
		violation.Existing = existing
	}
	violation.Fault = faultOfErr(err, Region{Start: start, End: end, Prot: prot})
	g.report(ctx, violation)
	return err
}

func (g *Guard) Check(ctx context.Context, address, length uint64, prot Prot) error {
	return g.inner.Check(ctx, address, length, prot)
}

func (g *Guard) Regions(ctx context.Context) ([]Region, error) { return g.inner.Regions(ctx) }

func (g *Guard) Account(ctx context.Context) (Account, error) { return g.inner.Account(ctx) }

func (g *Guard) existingAt(ctx context.Context, address uint64) (Region, bool) {
	regions, err := g.inner.Regions(ctx)
	if err != nil {
		return Region{}, false
	}
	for _, region := range regions {
		if region.Contains(address) {
			return region, true
		}
	}
	return Region{}, false
}

func faultOfErr(err error, _ Region) Fault {
	if err == nil {
		return 0
	}
	if isFault(err, errno.Permission) {
		return FaultProtection
	}
	if isFault(err, errno.Invalid) {

		return FaultMisaligned
	}
	return FaultUnmapped
}

var _ Space = (*Guard)(nil)
