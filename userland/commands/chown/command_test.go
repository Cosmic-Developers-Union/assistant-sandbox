package chown

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

type fixture struct {
	t      *testing.T
	system vmfs.System
}

func (f *fixture) handler(setup func(ctx context.Context, system vmfs.System)) commandio.Handler {
	return func(ctx context.Context, args []string) error {
		p, err := proc.FromContext(ctx)
		if err != nil {
			return err
		}
		set, ok := p.Resources.(capability.Set)
		if !ok {
			f.t.Fatalf("能力视图类型: %T", p.Resources)
		}
		system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
		if err != nil {
			f.t.Fatal(err)
		}
		if setup != nil {
			setup(ctx, system)
		}
		f.system = system
		set.FS = system
		p.Resources = set
		return Run(proc.WithContext(ctx, p), args)
	}
}

func (f *fixture) run(args ...string) commandtest.Result {
	return commandtest.RunWith(t2(f.t), f.handler(nil), f.t.TempDir(), args, "", commandtest.Options{Identity: identity.SingleUser{}})
}

func (f *fixture) runWithSetup(setup func(ctx context.Context, system vmfs.System), args ...string) commandtest.Result {
	return commandtest.RunWith(t2(f.t), f.handler(setup), f.t.TempDir(), args, "", commandtest.Options{Identity: identity.SingleUser{}})
}

func (f *fixture) ownerOf(name string) (uint32, uint32) {
	f.t.Helper()
	info, err := f.system.Lstat(context.Background(), name)
	if err != nil {
		f.t.Fatal(err)
	}
	meta := info.Sys().(*vmfs.Metadata)
	return meta.UID, meta.GID
}

func mustWrite(t *testing.T, ctx context.Context, system vmfs.System, name string) {
	t.Helper()
	if _, err := system.Open(ctx, name, vmfs.Create|vmfs.WriteOnly, 0o644); err != nil {
		t.Fatal(err)
	}
}

func t2(t *testing.T) *testing.T { return t }

func newFixture(t *testing.T) *fixture { return &fixture{t: t} }

func TestChownSpecs(t *testing.T) {
	f := newFixture(t)
	r := f.runWithSetup(func(ctx context.Context, system vmfs.System) {
		mustWrite(t, ctx, system, "/a.txt")
	}, "3", "/a.txt")
	if r.Code != 0 {
		t.Fatalf("chown 3 = %+v", r)
	}
	if uid, gid := f.ownerOf("/a.txt"); uid != 3 || gid != 0 {
		t.Fatalf("owner 形态 = %d:%d", uid, gid)
	}
	r = f.runWithSetup(func(ctx context.Context, system vmfs.System) {
		mustWrite(t, ctx, system, "/a.txt")
	}, "3:5", "/a.txt")
	if r.Code != 0 {
		t.Fatalf("chown 3:5 = %+v", r)
	}
	if uid, gid := f.ownerOf("/a.txt"); uid != 3 || gid != 5 {
		t.Fatalf("owner:group 形态 = %d:%d", uid, gid)
	}
	r = f.runWithSetup(func(ctx context.Context, system vmfs.System) {
		mustWrite(t, ctx, system, "/a.txt")
	}, ":7", "/a.txt")
	if r.Code != 0 {
		t.Fatalf("chown :7 = %+v", r)
	}
	if uid, gid := f.ownerOf("/a.txt"); uid != 0 || gid != 7 {
		t.Fatalf(":group 形态 = %d:%d", uid, gid)
	}
}

func TestChownRejectsNamesAndMissingCapability(t *testing.T) {
	f := newFixture(t)
	r := f.run("staff", "/a.txt")
	if r.Code != 1 || !strings.Contains(r.Stderr, "只接受数字 ID") {
		t.Fatalf("属主名 = %+v", r)
	}

	r = f.run(":", "/a.txt")
	if r.Code != 1 {
		t.Fatalf("空 spec = %+v", r)
	}

	r = commandtest.RunWith(t, Run, t.TempDir(), []string{"5", "a.txt"}, "",
		commandtest.Options{Identity: identity.SingleUser{}})
	if r.Code != 1 || !strings.Contains(r.Stderr, "所有权能力") {
		t.Fatalf("缺能力 = %+v", r)
	}

	r = f.run("99999999999", "/a.txt")
	if r.Code != 1 || !strings.Contains(r.Stderr, "越界") {
		t.Fatalf("越界 = %+v", r)
	}

	if r := f.run("5"); r.Code != 2 {
		t.Fatalf("缺操作数 = %+v", r)
	}
	if r := f.run("--bogus"); r.Code != 2 {
		t.Fatalf("未知选项 = %+v", r)
	}
}

func TestChownRecursiveSkipsSymlinks(t *testing.T) {
	f := newFixture(t)
	r := f.runWithSetup(func(ctx context.Context, system vmfs.System) {
		if err := system.Mkdir(ctx, "/dir", 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, ctx, system, "/dir/inner.txt")
		mustWrite(t, ctx, system, "/outside.txt")
		if err := system.Symlink(ctx, "/outside.txt", "/dir/link"); err != nil {
			t.Fatal(err)
		}
	}, "-R", "3:5", "/dir")
	if r.Code != 0 {
		t.Fatalf("chown -R = %+v", r)
	}
	if uid, gid := f.ownerOf("/dir"); uid != 3 || gid != 5 {
		t.Fatalf("目录属主 = %d:%d", uid, gid)
	}
	if uid, gid := f.ownerOf("/dir/inner.txt"); uid != 3 || gid != 5 {
		t.Fatalf("成员属主 = %d:%d", uid, gid)
	}

	if uid, gid := f.ownerOf("/outside.txt"); uid != 0 || gid != 0 {
		t.Fatalf("链接目标 = %d:%d，期望未被改动", uid, gid)
	}
}

func TestChownReportsFailures(t *testing.T) {
	f := newFixture(t)
	r := f.run("5", "/missing.txt")
	if r.Code != 1 || !strings.Contains(r.Stderr, "/missing.txt") {
		t.Fatalf("缺失文件 = %+v", r)
	}
	quiet := f.run("-f", "5", "/missing.txt")
	if quiet.Code != 1 || quiet.Stderr != "" {
		t.Fatalf("-f = %+v", quiet)
	}
}

type faultFS struct {
	vmfs.System
	lstatErr   error
	readdirErr error
	chownErr   error
}

func (f *faultFS) Lstat(ctx context.Context, name string) (fs.FileInfo, error) {
	if f.lstatErr != nil {
		return nil, f.lstatErr
	}
	return f.System.Lstat(ctx, name)
}
func (f *faultFS) ReadDir(ctx context.Context, name string) ([]fs.DirEntry, error) {
	if f.readdirErr != nil {
		return nil, f.readdirErr
	}
	return f.System.ReadDir(ctx, name)
}
func (f *faultFS) Chown(ctx context.Context, name string, uid, gid int64) error {
	if f.chownErr != nil {
		return f.chownErr
	}
	return f.System.(vmfs.Ownership).Chown(ctx, name, uid, gid)
}

func TestChgrpFaultPaths(t *testing.T) {
	ctx := context.Background()
	base, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := base.Mkdir(ctx, "/dir", 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, ctx, base, "/dir/x")

	run := func(system vmfs.System, args ...string) commandtest.Result {
		handler := func(ctx context.Context, args []string) error {
			p, err := proc.FromContext(ctx)
			if err != nil {
				return err
			}
			set := p.Resources.(capability.Set)
			set.FS = system
			p.Resources = set
			return Run(proc.WithContext(ctx, p), args)
		}
		return commandtest.RunWith(t, handler, t.TempDir(), args, "", commandtest.Options{Identity: identity.SingleUser{}})
	}

	for name, inject := range map[string]func(*faultFS){
		"lstat":   func(f *faultFS) { f.lstatErr = fs.ErrPermission },
		"readdir": func(f *faultFS) { f.readdirErr = fs.ErrPermission },
		"chown":   func(f *faultFS) { f.chownErr = fs.ErrPermission },
	} {
		fault := &faultFS{System: base}
		inject(fault)
		if r := run(fault, "-R", "5", "/dir"); r.Code != 1 {
			t.Fatalf("%s 故障 = %+v", name, r)
		}
	}

	oldNodes, oldDepth := walkNodes, walkDepth
	walkNodes, walkDepth = 1, 3
	defer func() { walkNodes, walkDepth = oldNodes, oldDepth }()
	if r := run(&faultFS{System: base}, "-R", "3:5", "/dir"); r.Code != 1 || !strings.Contains(r.Stderr, "预算") {
		t.Fatalf("预算 = %+v", r)
	}
}
