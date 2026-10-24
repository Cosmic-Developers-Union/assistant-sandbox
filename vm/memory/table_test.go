package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func page(n uint64) uint64 { return n * PageSize }

func TestMapRejectsMisalignedAndEmpty(t *testing.T) {
	ctx := context.Background()
	table := New(0)
	for _, tc := range []struct {
		label  string
		region Region
		want   error
	}{
		{"未对齐起点", Region{Start: 1, End: page(1), Prot: ProtRead}, errno.Invalid},
		{"未对齐终点", Region{Start: page(1), End: page(1) + 1, Prot: ProtRead}, errno.Invalid},
		{"空区间", Region{Start: page(1), End: page(1), Prot: ProtRead}, errno.Invalid},
		{"终点小于起点", Region{Start: page(2), End: page(1), Prot: ProtRead}, errno.Invalid},
		{"超出地址空间", Region{Start: maxAddress, End: maxAddress + PageSize, Prot: ProtRead}, errno.Invalid},
	} {
		if err := table.Map(ctx, tc.region); !errors.Is(err, tc.want) {
			t.Errorf("%s: err=%v want %v", tc.label, err, tc.want)
		}
	}

	if err := table.Map(ctx, Region{Start: page(1), End: page(2), Prot: 0}); err != nil {
		t.Fatalf("零保护位应可映射: %v", err)
	}
}

func TestMapRejectsWriteExecute(t *testing.T) {
	ctx := context.Background()
	table := New(0)
	err := table.Map(ctx, Region{Start: page(1), End: page(2), Prot: ProtRead | ProtWrite | ProtExec})
	if !errors.Is(err, errno.Permission) {
		t.Fatalf("W^X 应报 EACCES: %v", err)
	}

	for index, prot := range []Prot{ProtWrite, ProtExec, ProtRead | ProtWrite, ProtRead | ProtExec} {
		start := page(uint64(index + 10))
		if err := table.Map(ctx, Region{Start: start, End: start + PageSize, Prot: prot}); err != nil {
			t.Fatalf("prot=%s 应可映射: %v", prot, err)
		}
	}

	if err := table.Map(ctx, Region{Start: page(50), End: page(51), Prot: 1 << 7}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("未知保护位应报 EINVAL: %v", err)
	}
}

func TestMapRejectsOverlap(t *testing.T) {
	ctx := context.Background()
	table := New(0)
	if err := table.Map(ctx, Region{Start: page(1), End: page(4), Prot: ProtRead}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []Region{
		{Start: page(1), End: page(2), Prot: ProtRead},
		{Start: page(2), End: page(3), Prot: ProtRead},
		{Start: page(3), End: page(5), Prot: ProtRead},
		{Start: page(0), End: page(2), Prot: ProtRead},
		{Start: page(0), End: page(9), Prot: ProtRead},
	} {
		if err := table.Map(ctx, tc); !errors.Is(err, errno.Exist) {
			t.Fatalf("重叠 %d-%d 应报 EEXIST: %v", tc.Start, tc.End, err)
		}
	}

	if err := table.Map(ctx, Region{Start: page(4), End: page(5), Prot: ProtRead}); err != nil {
		t.Fatalf("紧邻映射应允许: %v", err)
	}
}

func TestMapRejectsOverBudget(t *testing.T) {
	ctx := context.Background()
	table := New(2 * PageSize)
	if err := table.Map(ctx, Region{Start: page(1), End: page(3), Prot: ProtRead}); err != nil {
		t.Fatal(err)
	}
	if err := table.Map(ctx, Region{Start: page(3), End: page(4), Prot: ProtRead}); !errors.Is(err, errno.NoSpace) {
		t.Fatalf("超预算应报 ENOSPC: %v", err)
	}
	account, err := table.Account(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if account.Mapped != 2*PageSize || account.Regions != 1 {
		t.Fatalf("被拒绝的映射不得计入: %+v", account)
	}

	if err := table.Unmap(ctx, page(1), page(3)); err != nil {
		t.Fatal(err)
	}
	if err := table.Map(ctx, Region{Start: page(3), End: page(4), Prot: ProtRead}); err != nil {
		t.Fatalf("释放后应可映射: %v", err)
	}
}

func TestCheckDistinguishesUnmappedAndProtection(t *testing.T) {
	ctx := context.Background()
	table := New(0)

	if err := table.Map(ctx, Region{Start: page(1), End: page(2), Prot: ProtRead}); err != nil {
		t.Fatal(err)
	}

	if err := table.Check(ctx, page(1), 16, ProtRead); err != nil {
		t.Fatalf("读应允许: %v", err)
	}

	err := table.Check(ctx, page(1), 16, ProtWrite)
	if !errors.Is(err, errno.Permission) {
		t.Fatalf("写只读区间应报 EACCES: %v", err)
	}
	if !contains(err.Error(), FaultProtection.String()) {
		t.Fatalf("错误应指明保护位故障: %v", err)
	}

	err = table.Check(ctx, page(9), 1, ProtRead)
	if !errors.Is(err, errno.Range) {
		t.Fatalf("未映射应报 ERANGE: %v", err)
	}
	if !contains(err.Error(), FaultUnmapped.String()) {
		t.Fatalf("错误应指明未映射: %v", err)
	}

	if err := table.Check(ctx, page(1)+PageSize-1, 2, ProtRead); !errors.Is(err, errno.Range) {
		t.Fatalf("跨边界访问应报 ERANGE: %v", err)
	}

	if err := table.Check(ctx, page(9), 0, ProtRead); err != nil {
		t.Fatalf("零长度应允许: %v", err)
	}

	if err := table.Check(ctx, ^uint64(0)-1, 8, ProtRead); !errors.Is(err, errno.Invalid) {
		t.Fatalf("溢出应报 EINVAL: %v", err)
	}

	if err := table.Check(ctx, page(1), 16, 0); err != nil {
		t.Fatalf("零权限查询应只判映射: %v", err)
	}
}

func TestUnmapSplitsRegionAndKeepsNeighbors(t *testing.T) {
	ctx := context.Background()
	table := New(0)
	if err := table.Map(ctx, Region{Start: page(1), End: page(4), Prot: ProtRead | ProtWrite}); err != nil {
		t.Fatal(err)
	}
	left := []byte("LEFT-DATA")
	right := []byte("RIGHT-DATA")
	if err := table.Write(ctx, page(1), left); err != nil {
		t.Fatal(err)
	}
	if err := table.Write(ctx, page(3), right); err != nil {
		t.Fatal(err)
	}

	if err := table.Unmap(ctx, page(2), page(3)); err != nil {
		t.Fatalf("中间页解除应成功: %v", err)
	}
	regions, err := table.Regions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(regions) != 2 || regions[0].Start != page(1) || regions[0].End != page(2) ||
		regions[1].Start != page(3) || regions[1].End != page(4) {
		t.Fatalf("应拆分为左右两段: %+v", regions)
	}

	got := make([]byte, len(left))
	if err := table.Read(ctx, page(1), got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(left) {
		t.Fatalf("左段数据被破坏: %q", got)
	}
	got = make([]byte, len(right))
	if err := table.Read(ctx, page(3), got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(right) {
		t.Fatalf("右段数据被破坏: %q", got)
	}
	if err := table.Check(ctx, page(2), 1, ProtRead); !errors.Is(err, errno.Range) {
		t.Fatalf("解除的中间页应未映射: %v", err)
	}
	if account, _ := table.Account(ctx); account.Mapped != 2*PageSize || account.Regions != 2 {
		t.Fatalf("解除后应只剩左右两页: %+v", account)
	}

	if err := table.Map(ctx, Region{Start: page(2), End: page(3), Prot: ProtRead}); err != nil {
		t.Fatalf("解除后应可重新映射: %v", err)
	}
	got = make([]byte, PageSize)
	if err := table.Read(ctx, page(2), got); err != nil {
		t.Fatal(err)
	}
	for _, b := range got {
		if b != 0 {
			t.Fatalf("重新映射的页应零填充: %x", got)
		}
	}
}

func TestUnmapFailsOnPartiallyCoveredRange(t *testing.T) {
	ctx := context.Background()
	table := New(0)
	if err := table.Map(ctx, Region{Start: page(1), End: page(2), Prot: ProtRead}); err != nil {
		t.Fatal(err)
	}
	if err := table.Map(ctx, Region{Start: page(3), End: page(4), Prot: ProtRead}); err != nil {
		t.Fatal(err)
	}

	if err := table.Unmap(ctx, page(1), page(4)); !errors.Is(err, errno.Range) {
		t.Fatalf("范围含空洞应整体失败: %v", err)
	}
	if err := table.Unmap(ctx, page(2), page(3)); !errors.Is(err, errno.Range) {
		t.Fatalf("完全未映射范围应失败: %v", err)
	}
	if err := table.Unmap(ctx, page(1)+1, page(2)); !errors.Is(err, errno.Invalid) {
		t.Fatalf("未对齐范围应报 EINVAL: %v", err)
	}
	regions, err := table.Regions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(regions) != 2 {
		t.Fatalf("失败时不得改动任何映射: %+v", regions)
	}

	if err := table.Map(ctx, Region{Start: page(2), End: page(3), Prot: ProtRead}); err != nil {
		t.Fatalf("补齐空洞后可整体解除: %v", err)
	}
	if err := table.Unmap(ctx, page(1), page(4)); err != nil {
		t.Fatalf("范围被完整覆盖后应可解除: %v", err)
	}
	if account, _ := table.Account(ctx); account.Mapped != 0 || account.Regions != 0 {
		t.Fatalf("释放后账户应清零: %+v", account)
	}
}

func TestProtectSplitsRegion(t *testing.T) {
	ctx := context.Background()
	table := New(0)
	if err := table.Map(ctx, Region{Start: page(1), End: page(5), Prot: ProtRead | ProtWrite}); err != nil {
		t.Fatal(err)
	}

	if err := table.Protect(ctx, page(2), page(4), ProtRead); err != nil {
		t.Fatal(err)
	}
	regions, err := table.Regions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(regions) != 3 {
		t.Fatalf("应切分为三段: %+v", regions)
	}
	if regions[0].Start != page(1) || regions[0].End != page(2) || !regions[0].Prot.CanWrite() {
		t.Fatalf("左段不符: %+v", regions[0])
	}
	if regions[1].Start != page(2) || regions[1].End != page(4) || regions[1].Prot.CanWrite() {
		t.Fatalf("中段应为只读: %+v", regions[1])
	}
	if regions[2].Start != page(4) || regions[2].End != page(5) || !regions[2].Prot.CanWrite() {
		t.Fatalf("右段不符: %+v", regions[2])
	}

	if err := table.Check(ctx, page(2), 1, ProtWrite); !errors.Is(err, errno.Permission) {
		t.Fatalf("中段写应被拒: %v", err)
	}
	if err := table.Check(ctx, page(1), 1, ProtWrite); err != nil {
		t.Fatalf("左段写应允许: %v", err)
	}

	table2 := New(0)
	if err := table2.Map(ctx, Region{Start: page(1), End: page(2), Prot: ProtRead}); err != nil {
		t.Fatal(err)
	}
	if err := table2.Protect(ctx, page(1), page(2), ProtRead|ProtWrite); err != nil {
		t.Fatal(err)
	}
	if regions, _ := table2.Regions(ctx); len(regions) != 1 {
		t.Fatalf("整段改权限不应分段: %+v", regions)
	}

	if err := table2.Protect(ctx, page(1), page(2), ProtWrite|ProtExec); !errors.Is(err, errno.Permission) {
		t.Fatalf("改权限为 W^X 应报 EACCES: %v", err)
	}

	if err := table2.Protect(ctx, page(9), page(10), ProtRead); !errors.Is(err, errno.Range) {
		t.Fatalf("未映射区间改权限应报 ERANGE: %v", err)
	}
}

func TestRegionsAndAccountAreCopies(t *testing.T) {
	ctx := context.Background()
	table := New(4 * PageSize)
	if err := table.Map(ctx, Region{Start: page(1), End: page(2), Prot: ProtRead, Name: "a"}); err != nil {
		t.Fatal(err)
	}
	regions, err := table.Regions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	regions[0].Prot = ProtRead | ProtWrite
	regions[0].Name = "被改写"
	again, err := table.Regions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again[0].Prot.CanWrite() || again[0].Name == "被改写" {
		t.Fatalf("Regions 必须返回副本: %+v", again[0])
	}
	account, err := table.Account(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if account.Limit != 4*PageSize {
		t.Fatalf("账户应报告上限: %+v", account)
	}
}

func TestContextCancellationIsHonoured(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	table := New(0)
	region := Region{Start: page(1), End: page(2), Prot: ProtRead}
	for _, label := range []string{"Map", "Unmap", "Protect", "Check", "Regions", "Account"} {
		var err error
		switch label {
		case "Map":
			err = table.Map(cancelled, region)
		case "Unmap":
			err = table.Unmap(cancelled, region.Start, region.End)
		case "Protect":
			err = table.Protect(cancelled, region.Start, region.End, ProtRead)
		case "Check":
			err = table.Check(cancelled, region.Start, 1, ProtRead)
		case "Regions":
			_, err = table.Regions(cancelled)
		case "Account":
			_, err = table.Account(cancelled)
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("%s: 取消应报取消，实际 %v", label, err)
		}
	}
}

func TestProtAndFaultNames(t *testing.T) {
	for prot, want := range map[Prot]string{
		0: "none", ProtRead: "r--", ProtWrite: "-w-", ProtExec: "--x",
		ProtRead | ProtWrite: "rw-", ProtRead | ProtExec: "r-x",
	} {
		if got := prot.String(); got != want {
			t.Errorf("Prot(%d).String()=%q want %q", prot, got, want)
		}
	}
	for fault, want := range map[Fault]string{
		FaultUnmapped: "unmapped", FaultProtection: "protection",
		FaultMisaligned: "misaligned", FaultOverflow: "overflow",
	} {
		if got := fault.String(); got != want {
			t.Errorf("Fault(%d).String()=%q want %q", fault, got, want)
		}
	}
	if got := Fault(99).String(); got != "fault(99)" {
		t.Fatalf("未知故障=%q", got)
	}

	if !(ProtWrite | ProtExec).WritableAndExecutable() {
		t.Fatal("写+执行应报告 W^X")
	}
	if (ProtRead | ProtExec).WritableAndExecutable() {
		t.Fatal("只读+执行不应报告 W^X")
	}
}

func TestRegionHelpers(t *testing.T) {
	region := Region{Start: 100, End: 200}
	if region.Size() != 100 {
		t.Fatalf("size=%d", region.Size())
	}
	if !region.Contains(100) || !region.Contains(199) {
		t.Fatal("区间内地址应包含")
	}
	if region.Contains(200) {
		t.Fatal("End 是开区间")
	}
	if !region.Overlaps(Region{Start: 150, End: 250}) {
		t.Fatal("相交应报告")
	}
	if region.Overlaps(Region{Start: 200, End: 300}) {
		t.Fatal("紧邻不应相交")
	}
	if region.Overlaps(Region{Start: 100, End: 100}) {
		t.Fatal("空区间不应相交")
	}
}

func contains(haystack, needle string) bool {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return true
		}
	}
	return false
}
