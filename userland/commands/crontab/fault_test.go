package crontab

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	platformfs "github.com/Cosmic-Developers-Union/assistant-sandbox/platform/filesystem"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

type failIdentity struct{}

func (failIdentity) Lookup(context.Context, uint64) (identity.Identity, error) {
	return identity.Identity{}, errors.New("注入的身份查询失败")
}

type failFS struct {
	vmfs.System
	mkdirErr  error
	writeErr  error
	renameErr error
	removeErr error
	readErr   error
}

func (f failFS) Mkdir(ctx context.Context, name string, mode fs.FileMode) error {
	if f.mkdirErr != nil {
		return f.mkdirErr
	}
	return f.System.Mkdir(ctx, name, mode)
}

func (f failFS) Rename(ctx context.Context, from, to string) error {
	if f.renameErr != nil {
		return f.renameErr
	}
	return f.System.Rename(ctx, from, to)
}

func (f failFS) Remove(ctx context.Context, name string) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	return f.System.Remove(ctx, name)
}

type brokenFile struct{ vmfs.File }

func (brokenFile) Read([]byte) (int, error) { return 0, errors.New("注入的读失败") }

func (f failFS) Open(ctx context.Context, name string, flags uint32, mode fs.FileMode) (vmfs.File, error) {
	if f.writeErr != nil && flags&vmfs.WriteOnly != 0 {
		return nil, f.writeErr
	}
	file, err := f.System.Open(ctx, name, flags, mode)
	if err != nil {
		return nil, err
	}
	if f.readErr != nil && flags == vmfs.ReadOnly {
		return brokenFile{file}, nil
	}
	return file, nil
}

func harness(t *testing.T, dir string, system vmfs.System, source identity.Source) commandio.Handler {
	t.Helper()
	return func(ctx context.Context, args []string) error {
		p, err := proc.FromContext(ctx)
		if err != nil {
			return err
		}
		set, ok := p.Resources.(capability.Set)
		if !ok {
			t.Fatalf("能力视图类型: %T", p.Resources)
		}
		set.FS = rootFS{inner: system, dir: dir}
		set.Identity = source
		p.Resources = set
		return Run(proc.WithContext(ctx, p), args)
	}
}

func TestFaultyOperations(t *testing.T) {
	dir := t.TempDir()
	base := platformfs.NewFilesystem(dir)
	options := commandtest.Options{Identity: identity.SingleUser{}}

	if err := commandtest.Invoke(t.Context(), harness(t, dir, base, failIdentity{}), dir, []string{"-l"}, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("身份查询失败应上抛")
	}

	if r := commandtest.RunWith(t, harness(t, dir, base, identity.SingleUser{}), dir, []string{"missing"}, "", options); r.Code != 2 {
		t.Fatalf("源文件缺失 = %+v", r)
	}

	faultyRead := failFS{System: base, readErr: errors.New("注入的读失败")}
	if err := os.WriteFile(filepath.Join(dir, "jobs"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := commandtest.Invoke(t.Context(), harness(t, dir, faultyRead, identity.SingleUser{}), dir, []string{"jobs"}, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("读失败应上抛")
	}

	wrapper := func(ctx context.Context, args []string) error {
		p, err := proc.FromContext(ctx)
		if err != nil {
			return err
		}
		set, ok := p.Resources.(capability.Set)
		if !ok {
			t.Fatalf("能力视图类型: %T", p.Resources)
		}
		set.FS = nil
		return Run(proc.WithContext(ctx, p), args)
	}
	if err := commandtest.Invoke(t.Context(), wrapper, dir, nil, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("文件能力缺失应上抛")
	}

	if err := os.MkdirAll(filepath.Join(dir, "var/cron/tabs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "var/cron/tabs/sandbox"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := commandtest.Invoke(t.Context(), harness(t, dir, base, identity.SingleUser{}), dir, []string{"-l"}, strings.NewReader(""), commandtest.FailWriter{}, io.Discard); err == nil {
		t.Fatal("下游写失败应上抛")
	}

	openFail := openFailFS{System: base}
	if err := commandtest.Invoke(t.Context(), harness(t, dir, openFail, identity.SingleUser{}), dir, []string{"-l"}, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("打开失败应上抛")
	}

	if err := commandtest.Invoke(t.Context(), harness(t, dir, failFS{System: base, removeErr: errors.New("注入的删除失败")}, identity.SingleUser{}), dir, []string{"-r"}, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("删除失败应上抛")
	}

	dir2 := t.TempDir()
	base2 := platformfs.NewFilesystem(dir2)
	if err := os.MkdirAll(filepath.Join(dir2, "var"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "var/cron"), []byte("占位"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := commandtest.Invoke(t.Context(), harness(t, dir2, base2, identity.SingleUser{}), dir2, nil, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("占位文件应导致「不是目录」错误")
	}
}

type openFailFS struct{ vmfs.System }

func (openFailFS) Open(context.Context, string, uint32, fs.FileMode) (vmfs.File, error) {
	return nil, errors.New("注入的打开失败")
}
