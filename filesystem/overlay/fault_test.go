package overlay

import (
	"context"
	"errors"
	stdfs "io/fs"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

type faulting struct {
	inner vmfs.System

	fails map[string]error
}

var ErrInjected = errors.New("注入的底层故障")

func (f *faulting) fail(method string) error {
	if err, ok := f.fails[method]; ok {
		return err
	}
	return nil
}

func (f *faulting) Open(ctx context.Context, name string, flags uint32, mode stdfs.FileMode) (vmfs.File, error) {
	if err := f.fail("Open"); err != nil {
		return nil, err
	}
	return f.inner.Open(ctx, name, flags, mode)
}

func (f *faulting) Stat(ctx context.Context, name string) (stdfs.FileInfo, error) {
	if err := f.fail("Stat"); err != nil {
		return nil, err
	}
	return f.inner.Stat(ctx, name)
}

func (f *faulting) Lstat(ctx context.Context, name string) (stdfs.FileInfo, error) {
	if err := f.fail("Lstat"); err != nil {
		return nil, err
	}
	return f.inner.Lstat(ctx, name)
}

func (f *faulting) ReadDir(ctx context.Context, name string) ([]stdfs.DirEntry, error) {
	if err := f.fail("ReadDir"); err != nil {
		return nil, err
	}
	return f.inner.ReadDir(ctx, name)
}

func (f *faulting) Mkdir(ctx context.Context, name string, mode stdfs.FileMode) error {
	if err := f.fail("Mkdir"); err != nil {
		return err
	}
	return f.inner.Mkdir(ctx, name, mode)
}

func (f *faulting) Remove(ctx context.Context, name string) error {
	if err := f.fail("Remove"); err != nil {
		return err
	}
	return f.inner.Remove(ctx, name)
}

func (f *faulting) Rename(ctx context.Context, from, to string) error {
	if err := f.fail("Rename"); err != nil {
		return err
	}
	return f.inner.Rename(ctx, from, to)
}

func (f *faulting) Link(ctx context.Context, from, to string) error {
	if err := f.fail("Link"); err != nil {
		return err
	}
	return f.inner.Link(ctx, from, to)
}

func (f *faulting) Symlink(ctx context.Context, target, link string) error {
	if err := f.fail("Symlink"); err != nil {
		return err
	}
	return f.inner.Symlink(ctx, target, link)
}

func (f *faulting) Readlink(ctx context.Context, name string) (string, error) {
	if err := f.fail("Readlink"); err != nil {
		return "", err
	}
	return f.inner.Readlink(ctx, name)
}

func newFaulting(t *testing.T, fails map[string]error) *faulting {
	t.Helper()
	inner, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return &faulting{inner: inner, fails: fails}
}

func TestFaultsArePropagated(t *testing.T) {
	ctx := context.Background()

	faultyUpper := newFaulting(t, map[string]error{"Lstat": ErrInjected})
	lower, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, lower, "/present", "lower")
	overlay, err := New(faultyUpper, lower)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := overlay.Stat(ctx, "/whatever"); !errors.Is(err, ErrInjected) {
		t.Errorf("Stat 应上报上层故障: %v", err)
	}
	if _, err := overlay.Lstat(ctx, "/whatever"); !errors.Is(err, ErrInjected) {
		t.Errorf("Lstat 应上报上层故障: %v", err)
	}
	if _, err := overlay.Open(ctx, "/whatever", vmfs.ReadOnly, 0); !errors.Is(err, ErrInjected) {
		t.Errorf("Open 应上报上层故障: %v", err)
	}
	if _, err := overlay.ReadDir(ctx, "/"); !errors.Is(err, ErrInjected) {
		t.Errorf("ReadDir 应上报上层故障: %v", err)
	}

	upper, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	faultyLower := newFaulting(t, map[string]error{"Lstat": ErrInjected})
	overlay2, err := New(upper, faultyLower)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := overlay2.Stat(ctx, "/whatever"); !errors.Is(err, ErrInjected) {
		t.Errorf("Stat 应上报下层故障: %v", err)
	}
	if _, err := overlay2.Lstat(ctx, "/whatever"); !errors.Is(err, ErrInjected) {
		t.Errorf("Lstat 应上报下层故障: %v", err)
	}

	faultyLowerOpen := newFaulting(t, map[string]error{"Open": ErrInjected})
	overlay3, err := New(upper, faultyLowerOpen)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := overlay3.Open(ctx, "/whatever", vmfs.ReadOnly, 0); !errors.Is(err, ErrInjected) {
		t.Errorf("Open 应上报下层 Open 故障: %v", err)
	}

	faultyLowerLink := newFaulting(t, map[string]error{"Readlink": ErrInjected})
	overlay4, err := New(upper, faultyLowerLink)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := overlay4.Readlink(ctx, "/whatever"); !errors.Is(err, ErrInjected) {
		t.Errorf("Readlink 应上报下层故障: %v", err)
	}

	faultyLowerDir := newFaulting(t, map[string]error{"ReadDir": ErrInjected})
	if err := faultyLowerDir.inner.Mkdir(ctx, "/listed", 0o755); err != nil {
		t.Fatal(err)
	}
	overlay5, err := New(upper, faultyLowerDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := overlay5.ReadDir(ctx, "/listed"); !errors.Is(err, ErrInjected) {
		t.Errorf("ReadDir 应上报下层故障: %v", err)
	}
}

func TestWritePathFaultsArePropagated(t *testing.T) {
	ctx := context.Background()

	upper, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	lower, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, lower, "/deep/file", "content")
	faultyLower := &faulting{inner: lower, fails: nil}
	faultyUpper := &faulting{inner: upper, fails: map[string]error{"Mkdir": ErrInjected}}
	overlay, err := New(faultyUpper, faultyLower)
	if err != nil {
		t.Fatal(err)
	}
	if err := overlay.Mkdir(ctx, "/deep/new", 0o755); !errors.Is(err, ErrInjected) {
		t.Fatalf("Mkdir 故障应上报: %v", err)
	}

	faultyUpper2 := &faulting{inner: upper, fails: map[string]error{"Open": ErrInjected}}
	overlay2, err := New(faultyUpper2, lower)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := overlay2.Open(ctx, "/deep/file", vmfs.WriteOnly, 0); !errors.Is(err, ErrInjected) {
		t.Fatalf("写路径 Open 故障应上报: %v", err)
	}

	faultyUpper3 := &faulting{inner: upper, fails: map[string]error{"Remove": ErrInjected}}
	overlay3, err := New(faultyUpper3, lower)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, upper, "/victim", "x")
	if err := overlay3.Remove(ctx, "/victim"); !errors.Is(err, ErrInjected) {
		t.Fatalf("Remove 故障应上报: %v", err)
	}

	faultyUpper4 := &faulting{inner: upper, fails: map[string]error{"Rename": ErrInjected}}
	overlay4, err := New(faultyUpper4, lower)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, upper, "/mv-src", "x")
	if err := overlay4.Rename(ctx, "/mv-src", "/mv-dst"); !errors.Is(err, ErrInjected) {
		t.Fatalf("Rename 故障应上报: %v", err)
	}

	faultyUpper5 := &faulting{inner: upper, fails: map[string]error{"Link": ErrInjected}}
	overlay5, err := New(faultyUpper5, lower)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, upper, "/ln-src", "x")
	if err := overlay5.Link(ctx, "/ln-src", "/ln-dst"); !errors.Is(err, ErrInjected) {
		t.Fatalf("Link 故障应上报: %v", err)
	}

	faultyUpper6 := &faulting{inner: upper, fails: map[string]error{"Symlink": ErrInjected}}
	overlay6, err := New(faultyUpper6, lower)
	if err != nil {
		t.Fatal(err)
	}
	if err := overlay6.Symlink(ctx, "/target", "/sl"); !errors.Is(err, ErrInjected) {
		t.Fatalf("Symlink 故障应上报: %v", err)
	}

	faultyLower2 := &faulting{inner: lower, fails: map[string]error{"ReadDir": ErrInjected}}
	overlay7, err := New(upper, faultyLower2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := overlay7.ReadDir(ctx, "/deep"); !errors.Is(err, ErrInjected) {
		t.Fatalf("ReadDir 故障应上报: %v", err)
	}

	faultyLower3 := &faulting{inner: lower, fails: map[string]error{"Readlink": ErrInjected}}
	overlay8, err := New(upper, faultyLower3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := overlay8.Readlink(ctx, "/deep/file"); err == nil {
		t.Fatal("Readlink 故障应上报")
	}
}

func TestLowerOnlyAndShadowHelpers(t *testing.T) {
	ctx := context.Background()

	upper, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	lower, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, lower, "/thing", "x")
	faultyUpper := &faulting{inner: upper, fails: map[string]error{"Lstat": ErrInjected}}
	overlay, err := New(faultyUpper, lower)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := overlay.isWhiteout(ctx, "/thing"); !errors.Is(err, ErrInjected) {
		t.Fatalf("遮蔽查询故障应上报: %v", err)
	}
	if _, err := overlay.inLowerOnly(ctx, "/thing"); !errors.Is(err, ErrInjected) {
		t.Fatalf("下层独占判定故障应上报: %v", err)
	}

	plain, err := New(upper, lower)
	if err != nil {
		t.Fatal(err)
	}
	if err := plain.placeWhiteout(ctx, "/"); !errors.Is(err, errno.Invalid) {
		t.Fatalf("遮蔽根应报 EINVAL: %v", err)
	}

	if dir, name := splitPath("/"); dir != "/" || name != "" {
		t.Fatalf("splitPath(\"/\")=%q %q", dir, name)
	}
	if dir, name := splitPath("relative"); dir != "" || name != "" {
		t.Fatalf("相对路径应被拒绝: %q %q", dir, name)
	}
	if dir, name := splitPath(""); dir != "" || name != "" {
		t.Fatalf("空路径应被拒绝: %q %q", dir, name)
	}
	if joined := joinPath("/", "x"); joined != "/x" {
		t.Fatalf("joinPath=%q", joined)
	}
}
