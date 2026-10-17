package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type recorder struct {
	violations []Violation
}

func (r *recorder) report(_ context.Context, violation Violation) {
	r.violations = append(r.violations, violation)
}

func TestGuardReportsWriteExecuteAttempts(t *testing.T) {
	ctx := context.Background()
	sink := &recorder{}
	space := NewGuard(New(0), sink.report)

	err := space.Map(ctx, Region{Start: page(1), End: page(2), Prot: ProtRead | ProtWrite | ProtExec})
	if !errors.Is(err, errno.Permission) {
		t.Fatalf("W^X 应报 EACCES: %v", err)
	}
	if len(sink.violations) != 1 {
		t.Fatalf("应上报一次违规: %+v", sink.violations)
	}
	violation := sink.violations[0]
	if violation.Start != page(1) || violation.End != page(2) {
		t.Fatalf("违规区间不符: %+v", violation)
	}
	if !violation.Requested.WritableAndExecutable() {
		t.Fatalf("违规应记录请求的保护位: %+v", violation)
	}
	if violation.Fault != FaultProtection {
		t.Fatalf("故障类别应为保护位: %+v", violation)
	}

	regions, err := space.Regions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(regions) != 0 {
		t.Fatalf("被拒绝的映射不得生效: %+v", regions)
	}
}

func TestGuardReportsOverlapAndMisalignment(t *testing.T) {
	ctx := context.Background()
	sink := &recorder{}
	space := NewGuard(New(0), sink.report)
	if err := space.Map(ctx, Region{Start: page(1), End: page(3), Prot: ProtRead, Name: "held"}); err != nil {
		t.Fatal(err)
	}

	if err := space.Map(ctx, Region{Start: page(2), End: page(4), Prot: ProtRead}); !errors.Is(err, errno.Exist) {
		t.Fatalf("重叠应报 EEXIST: %v", err)
	}

	if err := space.Protect(ctx, page(1), page(2), ProtWrite|ProtExec); !errors.Is(err, errno.Permission) {
		t.Fatalf("改权限为 W^X 应报 EACCES: %v", err)
	}
	if len(sink.violations) != 2 {
		t.Fatalf("应上报两次: %+v", sink.violations)
	}
	if sink.violations[1].Existing.Name != "held" {
		t.Fatalf("Protect 违规应带上既有映射: %+v", sink.violations[1])
	}

	if err := space.Map(ctx, Region{Start: 1, End: page(1), Prot: ProtRead}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("未对齐应报 EINVAL: %v", err)
	}
	if sink.violations[2].Fault != FaultMisaligned {
		t.Fatalf("未对齐的故障类别不符: %+v", sink.violations[2])
	}
	if err := space.Unmap(ctx, page(9), page(10)); !errors.Is(err, errno.Range) {
		t.Fatalf("未映射解除应报 ERANGE: %v", err)
	}
	if len(sink.violations) != 4 {
		t.Fatalf("应累计四次上报: %+v", sink.violations)
	}
	if sink.violations[3].Fault != FaultUnmapped {
		t.Fatalf("未映射的故障类别不符: %+v", sink.violations[3])
	}
}

func TestGuardDoesNotReportReads(t *testing.T) {
	ctx := context.Background()
	sink := &recorder{}
	space := NewGuard(New(0), sink.report)

	if err := space.Check(ctx, page(9), 1, ProtRead); err == nil {
		t.Fatal("未映射读应失败")
	}
	if err := space.Check(ctx, page(9), 1, ProtWrite); err == nil {
		t.Fatal("未映射写应失败")
	}
	if len(sink.violations) != 0 {
		t.Fatalf("读判定不应上报: %+v", sink.violations)
	}
}

func TestGuardWithoutReporterReportsNothing(t *testing.T) {
	ctx := context.Background()
	space := NewGuard(New(0), nil)
	if err := space.Map(ctx, Region{Start: page(1), End: page(2), Prot: ProtWrite | ProtExec}); !errors.Is(err, errno.Permission) {
		t.Fatalf("W^X 应仍被拒绝: %v", err)
	}

	zero := NewGuard(nil, nil)
	if err := zero.Map(ctx, Region{Start: page(1), End: page(2), Prot: ProtRead}); err != nil {
		t.Fatalf("nil 内部表应可用: %v", err)
	}
}

func TestGuardDelegatesReadOnlyQueries(t *testing.T) {
	ctx := context.Background()
	inner := New(4 * PageSize)
	space := NewGuard(inner, nil)
	if err := space.Map(ctx, Region{Start: page(1), End: page(2), Prot: ProtRead}); err != nil {
		t.Fatal(err)
	}
	regions, err := space.Regions(ctx)
	if err != nil || len(regions) != 1 {
		t.Fatalf("regions=%+v err=%v", regions, err)
	}
	account, err := space.Account(ctx)
	if err != nil || account.Mapped != PageSize || account.Limit != 4*PageSize {
		t.Fatalf("account=%+v err=%v", account, err)
	}

	if err := space.Check(ctx, page(1), 8, ProtRead); err != nil {
		t.Fatalf("读应允许: %v", err)
	}
	if err := space.Check(ctx, page(1), 8, ProtWrite); !errors.Is(err, errno.Permission) {
		t.Fatalf("写应报 EACCES: %v", err)
	}
}
