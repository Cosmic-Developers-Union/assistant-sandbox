package persistent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// failingCommitFS 在指定提交步骤注入失败(I17):模拟写入关键步骤
// 的中断;其余步骤透传宿主文件系统。
type failingCommitFS struct {
	host     hostCommitFS
	failStep string
}

// 提交步骤名:与 commitAtomic 的调用序列一致。
const (
	stepCreate  = "create"
	stepWrite   = "write"
	stepSync    = "sync"
	stepClose   = "close"
	stepChmod   = "chmod"
	stepRename  = "rename"
	stepDirSync = "dirsync"
)

var commitSteps = []string{stepCreate, stepWrite, stepSync, stepClose, stepChmod, stepRename, stepDirSync}

func (f *failingCommitFS) inject(step string) error {
	if step == f.failStep {
		return errors.New("injected failure: " + step)
	}
	return nil
}

func (f *failingCommitFS) CreateTemp(dir, pattern string) (*os.File, error) {
	if err := f.inject(stepCreate); err != nil {
		return nil, err
	}
	return f.host.CreateTemp(dir, pattern)
}

func (f *failingCommitFS) Write(file *os.File, data []byte) error {
	if err := f.inject(stepWrite); err != nil {
		return err
	}
	return f.host.Write(file, data)
}

func (f *failingCommitFS) Sync(file *os.File) error {
	if err := f.inject(stepSync); err != nil {
		return err
	}
	return f.host.Sync(file)
}

func (f *failingCommitFS) Close(file *os.File) error {
	if err := f.inject(stepClose); err != nil {
		return err
	}
	return f.host.Close(file)
}

func (f *failingCommitFS) Chmod(name string, mode os.FileMode) error {
	if err := f.inject(stepChmod); err != nil {
		return err
	}
	return f.host.Chmod(name, mode)
}

func (f *failingCommitFS) Rename(oldname, newname string) error {
	if err := f.inject(stepRename); err != nil {
		return err
	}
	return f.host.Rename(oldname, newname)
}

func (f *failingCommitFS) SyncDir(dir string) error {
	if err := f.inject(stepDirSync); err != nil {
		return err
	}
	return f.host.SyncDir(dir)
}

func (f *failingCommitFS) Remove(name string) error { return f.host.Remove(name) }

// TestSaveInterruptionAtEveryStepKeepsDefinedState 验证 I17 主验收:
// 在写入关键步骤逐个注入中断后,"重启"(重新 New)只出现已定义的
// 一致状态——目标保持旧版本完整或不存在,无临时残留,后续 Save
// 与 Load 正常。
func TestSaveInterruptionAtEveryStepKeepsDefinedState(t *testing.T) {
	ctx := context.Background()
	for _, step := range commitSteps {
		t.Run(step, func(t *testing.T) {
			root := t.TempDir()
			store, err := New(root)
			if err != nil {
				t.Fatal(err)
			}
			// 旧版本先落盘。
			if err := store.Save(ctx, "s", []byte("v1")); err != nil {
				t.Fatal(err)
			}
			// 注入中断的提交层覆盖默认实现。
			store.commit = &failingCommitFS{failStep: step}
			err = store.Save(ctx, "s", []byte("v2-newer-longer"))
			if err == nil {
				t.Fatalf("步骤 %s 注入失败后 Save 应报错", step)
			}
			// 重启:恢复过程丢弃临时残留。
			reopened, err := New(root)
			if err != nil {
				t.Fatalf("重启应成功: %v", err)
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), tempPrefix) {
					t.Fatalf("重启后不得残留临时文件: %s", entry.Name())
				}
			}
			// 已定义状态:v2 从未半途可见——重命名前的中断保持
			// 旧版本,dirsync 中断(重命名已发生)保持完整新版本。
			data, err := reopened.Load(ctx, "s")
			if err != nil {
				t.Fatalf("重启后读取: %v", err)
			}
			want := "v1"
			if step == stepDirSync {
				want = "v2-newer-longer"
			}
			if string(data) != want {
				t.Fatalf("重启后内容=%q want %q", data, want)
			}
			names, err := reopened.List(ctx)
			if err != nil || len(names) != 1 || names[0] != "s" {
				t.Fatalf("重启后列表=%v err=%v", names, err)
			}
			// 存储继续可用。
			if err := reopened.Save(ctx, "s", []byte("v3")); err != nil {
				t.Fatalf("恢复后保存: %v", err)
			}
			if data, err := reopened.Load(ctx, "s"); err != nil || string(data) != "v3" {
				t.Fatalf("恢复后读写=%q err=%v", data, err)
			}
		})
	}
}

// TestFirstSaveInterruptionLeavesNoSession 验证首次保存中断:重启后
// 会话不存在(ENOENT),不得出现半成品。
func TestFirstSaveInterruptionLeavesNoSession(t *testing.T) {
	ctx := context.Background()
	for _, step := range commitSteps {
		t.Run(step, func(t *testing.T) {
			root := t.TempDir()
			store, err := New(root)
			if err != nil {
				t.Fatal(err)
			}
			store.commit = &failingCommitFS{failStep: step}
			if err := store.Save(ctx, "fresh", []byte("data")); err == nil {
				t.Fatalf("步骤 %s 注入失败后 Save 应报错", step)
			}
			reopened, err := New(root)
			if err != nil {
				t.Fatal(err)
			}
			data, err := reopened.Load(ctx, "fresh")
			switch step {
			case stepDirSync:
				// 目录同步在重命名之后:提交已完成,新版本完整
				// 可读(仅持久性屏障未走完)——同样是已定义状态。
				if err != nil || string(data) != "data" {
					t.Fatalf("dirsync 中断后应读到完整新版本=%q err=%v", data, err)
				}
			default:
				// 重命名之前:目标保持不存在,无半成品。
				if !errors.Is(err, errno.NotFound) {
					t.Fatalf("首次保存中断后应报 ENOENT: %v", err)
				}
			}
			names, err := reopened.List(ctx)
			want := 0
			if step == stepDirSync {
				want = 1
			}
			if err != nil || len(names) != want {
				t.Fatalf("列表=%v err=%v want %d 项", names, err, want)
			}
		})
	}
}

// TestPutInterruptionKeepsSnapshotAllOrNothing 验证快照发布的逐步
// 中断:重启后快照要么不存在要么完整可读(内容寻址自校验)。
func TestPutInterruptionKeepsSnapshotAllOrNothing(t *testing.T) {
	ctx := context.Background()
	for _, step := range commitSteps {
		t.Run(step, func(t *testing.T) {
			root := t.TempDir()
			store, err := New(root)
			if err != nil {
				t.Fatal(err)
			}
			store.commit = &failingCommitFS{failStep: step}
			if _, err := store.Put(ctx, []byte("snapshot-body")); err == nil {
				t.Fatalf("步骤 %s 注入失败后 Put 应报错", step)
			}
			reopened, err := New(root)
			if err != nil {
				t.Fatal(err)
			}
			digest := digestPrefix + strings.Repeat("0", 0)
			_ = digest
			// 快照标识未知(发布未完成)——按内容寻址语义,任何
			// 标识要么完整要么不存在;已知内容重试必须成功且幂等。
			second, err := reopened.Put(ctx, []byte("snapshot-body"))
			if err != nil {
				t.Fatalf("恢复后重试发布: %v", err)
			}
			again, err := reopened.Put(ctx, []byte("snapshot-body"))
			if err != nil || again != second {
				t.Fatalf("重复发布应幂等: %q vs %q err=%v", again, second, err)
			}
			data, err := reopened.Get(ctx, second)
			if err != nil || string(data) != "snapshot-body" {
				t.Fatalf("读取=%q err=%v", data, err)
			}
			// 不存在内容的读取明确 ENOENT。
			missing := digestPrefix + strings.Repeat("f", 64)
			if _, err := reopened.Get(ctx, missing); !errors.Is(err, errno.NotFound) {
				t.Fatalf("未知快照应 ENOENT: %v", err)
			}
		})
	}
}

// TestDeleteInterruptionLeavesQueryableState 验证删除的目录同步步骤
// 中断:重启后会话要么已删除要么仍完整可读(内容未被半改)。
func TestDeleteInterruptionLeavesQueryableState(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, "s", []byte("kept-or-gone")); err != nil {
		t.Fatal(err)
	}
	store.commit = &failingCommitFS{failStep: stepDirSync}
	if err := store.Delete(ctx, "s"); err == nil {
		t.Fatal("注入失败后 Delete 应报错")
	}
	reopened, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	// 已定义状态:文件已移除(unlink 先于目录同步)——ENOENT;
	// 若宿主语义保留条目,则内容必须完整可读。两者皆为一致。
	data, err := reopened.Load(ctx, "s")
	if err != nil {
		if !errors.Is(err, errno.NotFound) {
			t.Fatalf("重启后读取: %v", err)
		}
		return
	}
	if string(data) != "kept-or-gone" {
		t.Fatalf("残留内容=%q", data)
	}
}

// TestRecoveryDiscardsOnlyTempPrefix 验证恢复只清理临时前缀,
// 不触碰会话与快照。
func TestRecoveryDiscardsOnlyTempPrefix(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, "s", []byte("keep")); err != nil {
		t.Fatal(err)
	}
	digest, err := store.Put(ctx, []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	// 模拟上次中断的临时残留。
	if err := os.WriteFile(filepath.Join(root, tempPrefix+"s.session-xyz"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, tempPrefix+"s.session-xyz")); !os.IsNotExist(err) {
		t.Fatalf("残留应被清理: %v", err)
	}
	if data, err := reopened.Load(ctx, "s"); err != nil || string(data) != "keep" {
		t.Fatalf("会话应保留=%q err=%v", data, err)
	}
	if snap, err := reopened.Get(ctx, digest); err != nil || string(snap) != "snap" {
		t.Fatalf("快照应保留=%q err=%v", snap, err)
	}
}
