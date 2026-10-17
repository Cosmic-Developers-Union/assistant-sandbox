package executable

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/digest"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

func write(t *testing.T, system vmfs.System, path, content string) {
	t.Helper()
	file, err := system.Open(context.Background(), path, vmfs.Create|vmfs.ReadWrite, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func registryWith(entries map[string]Program) *Static {
	reg, err := New(entries)
	if err != nil {
		panic(err)
	}
	return reg
}

func TestRegistryLookupAndIdentity(t *testing.T) {
	reg := registryWith(map[string]Program{"probe": Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil })})
	resolver := NewResolver(reg, nil)
	resolved, err := resolver.Resolve(context.Background(), "probe")
	if err != nil || resolved.ABI != ABIBuiltin || resolved.Name != "probe" {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}

	if _, err := resolver.Resolve(context.Background(), "missing"); !errors.Is(err, errno.NotFound) {
		t.Fatal(err)
	}
}

func TestShebangResolution(t *testing.T) {
	clk := &clock.Virtual{}
	system, err := memfs.New(clk, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Mkdir(context.Background(), "/scripts", 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, system, "/scripts/run.sh", "#!interp\n")
	write(t, system, "/scripts/arg.sh", "#!interp -x\n")
	write(t, system, "/scripts/plain.txt", "echo hi\n")
	write(t, system, "/scripts/empty.sh", "")
	write(t, system, "/scripts/long.sh", "#!interp a b\n")
	reg := registryWith(map[string]Program{"interp": Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil })})
	resolver := NewResolver(reg, system)
	ctx := context.Background()

	resolved, err := resolver.Resolve(ctx, "/scripts/run.sh")
	if err != nil || resolved.ABI != ABIShebang || resolved.Name != "interp" || len(resolved.Args) != 1 || resolved.Args[0] != "/scripts/run.sh" {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	resolved, err = resolver.Resolve(ctx, "/scripts/arg.sh")
	if err != nil || len(resolved.Args) != 2 || resolved.Args[0] != "-x" {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}

	for _, path := range []string{"/scripts/plain.txt", "/scripts/empty.sh", "/scripts/long.sh"} {
		if _, err := resolver.Resolve(ctx, path); !errors.Is(err, errno.NoExec) {
			t.Fatalf("%s: %v", path, err)
		}
	}

	write(t, system, "/scripts/bad.sh", "#!ghost\n")
	if _, err := resolver.Resolve(ctx, "/scripts/bad.sh"); !errors.Is(err, errno.NotFound) {
		t.Fatal(err)
	}

	write(t, system, "/run.sh", "#!interp\n")
	if _, err := resolver.Resolve(ctx, "run.sh"); !errors.Is(err, errno.NotFound) {
		t.Fatal(err)
	}

	if _, err := resolver.Resolve(ctx, "/scripts/ghost.sh"); !errors.Is(err, errno.NotFound) {
		t.Fatal(err)
	}

	if _, err := resolver.Resolve(ctx, "scripts/run.sh"); !errors.Is(err, errno.NotFound) {
		t.Fatal(err)
	}
}

func newScriptFS(t *testing.T, scripts map[string]string) vmfs.System {
	t.Helper()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range scripts {

		if dir := parentDir(path); dir != "/" {
			if err := system.Mkdir(context.Background(), dir, 0o755); err != nil && !errors.Is(err, errno.Exist) {
				t.Fatal(err)
			}
		}
		write(t, system, path, content)
	}
	return system
}

func parentDir(path string) string {
	for i := len(path) - 1; i > 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "/"
}

const interpreterScript = "#!/bin/bash\n"

func newBoundResolver(t *testing.T, scripts map[string]string) (*Resolver, *memoryImage) {
	t.Helper()
	all := map[string]string{"/bin/sh": interpreterScript}
	for path, content := range scripts {
		all[path] = content
	}
	return newBoundResolverFor(t, all)
}

func newBoundResolverFor(t *testing.T, scripts map[string]string) (*Resolver, *memoryImage) {
	t.Helper()
	system := newScriptFS(t, scripts)
	registry, err := New(map[string]Program{
		"sh":   Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
		"bash": Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	img := &memoryImage{digests: map[string]string{}}
	for path, content := range scripts {
		img.digests[path] = digest.Content([]byte(content))
	}
	return NewBoundResolver(registry, system, img), img
}

type memoryImage struct{ digests map[string]string }

func (m *memoryImage) DigestFor(_ context.Context, path string) (string, error) {
	value, ok := m.digests[path]
	if !ok {
		return "", errno.NotFound
	}
	return value, nil
}

func TestBoundResolverAcceptsMatchingContent(t *testing.T) {
	resolver, _ := newBoundResolver(t, map[string]string{"/bin/tool": "#!/bin/sh\necho hi\n"})
	resolved, err := resolver.Resolve(t.Context(), "/bin/tool")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ABI != ABIShebang || resolved.ContentDigest == "" {
		t.Fatalf("resolved=%+v", resolved)
	}
	if resolved.ContentDigest != digest.Content([]byte("#!/bin/sh\necho hi\n")) {
		t.Fatalf("内容身份不符: %s", resolved.ContentDigest)
	}
}

func TestBoundResolverRejectsChangedContent(t *testing.T) {
	resolver, img := newBoundResolver(t, map[string]string{"/bin/tool": "#!/bin/sh\necho hi\n"})

	img.digests["/bin/tool"] = digest.Content([]byte("#!/bin/sh\necho tampered\n"))
	if _, err := resolver.Resolve(t.Context(), "/bin/tool"); !errors.Is(err, errno.NoExec) {
		t.Fatalf("内容被改写应报 ENOEXEC: %v", err)
	}
}

func TestBoundResolverRejectsScriptNotInImage(t *testing.T) {

	resolver, _ := newBoundResolverFor(t, map[string]string{
		"/bin/sh":    interpreterScript,
		"/bin/tool":  "#!/bin/sh\necho tool\n",
		"/bin/other": "#!/bin/sh\necho other\n",
	})

	system := newScriptFS(t, map[string]string{
		"/bin/sh":    interpreterScript,
		"/bin/tool":  "#!/bin/sh\necho tool\n",
		"/bin/other": "#!/bin/sh\necho other\n",
	})
	registry, err := New(map[string]Program{
		"sh":   Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
		"bash": Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	img := &memoryImage{digests: map[string]string{
		"/bin/sh":   digest.Content([]byte(interpreterScript)),
		"/bin/tool": digest.Content([]byte("#!/bin/sh\necho tool\n")),
	}}
	bound := NewBoundResolver(registry, system, img)
	if _, err := bound.Resolve(t.Context(), "/bin/other"); !errors.Is(err, errno.NoExec) {
		t.Fatalf("镜像外脚本应报 ENOEXEC: %v", err)
	}

	if _, err := resolver.Resolve(t.Context(), "/bin/tool"); err != nil {
		t.Fatalf("镜像内脚本应可解析: %v", err)
	}
}

func TestUnboundResolverKeepsPreviousBehavior(t *testing.T) {

	system := newScriptFS(t, map[string]string{"/bin/tool": "#!/bin/sh\necho hi\n", "/bin/sh": interpreterScript})
	registry, err := New(map[string]Program{
		"sh":   Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
		"bash": Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := NewResolver(registry, system).Resolve(t.Context(), "/bin/tool")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ContentDigest != "" {
		t.Fatalf("未绑定时不应声称内容身份: %+v", resolved)
	}
}

func TestShebangInterpreterPathFormResolves(t *testing.T) {
	system := newScriptFS(t, map[string]string{"/run.sh": "#!/bin/bash\necho hi\n"})
	registry, err := New(map[string]Program{"bash": Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil })})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := NewResolver(registry, system).Resolve(t.Context(), "/run.sh")
	if err != nil {
		t.Fatal(err)
	}

	if resolved.Name != "bash" || resolved.ABI != ABIShebang || resolved.Program == nil {
		t.Fatalf("路径形式 shebang 应回落到登记的 bash: %+v", resolved)
	}
	if len(resolved.Args) != 1 || resolved.Args[0] != "/run.sh" {
		t.Fatalf("参数不符: %v", resolved.Args)
	}

	write(t, system, "/arg.sh", "#!/bin/bash -e\necho hi\n")
	resolved, err = NewResolver(registry, system).Resolve(t.Context(), "/arg.sh")
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Args) != 2 || resolved.Args[0] != "-e" || resolved.Args[1] != "/arg.sh" {
		t.Fatalf("参数不符: %v", resolved.Args)
	}
}

func TestExistingFileTakesPrecedenceOverRegistry(t *testing.T) {
	registry, err := New(map[string]Program{
		"bash": Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
		"loop": Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}

	system := newScriptFS(t, map[string]string{"/bin/loop": "#!/bin/bash\n", "/tool": "#!/bin/loop\necho hi\n"})
	resolved, err := NewResolver(registry, system).Resolve(t.Context(), "/tool")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Name != "bash" || resolved.ABI != ABIShebang || resolved.Program == nil {
		t.Fatalf("存在的解释器脚本应按脚本解析到底: %+v", resolved)
	}

	system = newScriptFS(t, map[string]string{"/bin/bash": "just text, no shebang\n"})
	if _, err := NewResolver(registry, system).Resolve(t.Context(), "/bin/bash"); !errors.Is(err, errno.NoExec) {
		t.Fatalf("存在的非可执行文件应报 ENOEXEC: %v", err)
	}

	system = newScriptFS(t, map[string]string{"/run.sh": "#!/bin/ghost-interp\n"})
	if _, err := NewResolver(registry, system).Resolve(t.Context(), "/run.sh"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("未登记解释器应拒绝: %v", err)
	}
}

type binaryFactory struct {
	paths   []string
	program Program
	err     error
}

func (f *binaryFactory) New(path string) (Program, []string, error) {
	f.paths = append(f.paths, path)
	if f.err != nil {
		return nil, nil, f.err
	}
	return f.program, []string{path}, nil
}

func elfImage() string {
	return "\x7fELF\x02\x01\x01\x00" + strings.Repeat("\x00", 56)
}

func TestBinaryImageResolution(t *testing.T) {
	program := Func(func(context.Context, proc.Proc) (uint8, error) { return 7, nil })
	factory := &binaryFactory{program: program}
	system := newScriptFS(t, map[string]string{"/program": elfImage()})
	resolver := NewResolver(registryWith(nil), system).WithBinaries(factory)
	resolved, err := resolver.Resolve(context.Background(), "/program")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ABI != ABIELF || resolved.Name != "/program" || resolved.Program == nil {
		t.Fatalf("解析=%+v", resolved)
	}
	if len(resolved.Args) != 1 || resolved.Args[0] != "/program" {
		t.Fatalf("参数=%v", resolved.Args)
	}
	if len(factory.paths) != 1 || factory.paths[0] != "/program" {
		t.Fatalf("执行器收到=%v", factory.paths)
	}
}

func TestBinaryImageNeedsFactory(t *testing.T) {
	system := newScriptFS(t, map[string]string{"/program": elfImage()})
	resolver := NewResolver(registryWith(nil), system)
	if _, err := resolver.Resolve(context.Background(), "/program"); !errors.Is(err, errno.NoExec) {
		t.Fatalf("err=%v want ENOEXEC", err)
	}
}

func TestBinaryImageFactoryErrorIsPropagated(t *testing.T) {
	factory := &binaryFactory{err: errno.NotSupported}
	system := newScriptFS(t, map[string]string{"/program": elfImage()})
	resolver := NewResolver(registryWith(nil), system).WithBinaries(factory)
	if _, err := resolver.Resolve(context.Background(), "/program"); !errors.Is(err, errno.NotSupported) {
		t.Fatalf("err=%v want ENOTSUP", err)
	}
}

func TestUnknownFormatIsRejected(t *testing.T) {
	factory := &binaryFactory{program: Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil })}
	system := newScriptFS(t, map[string]string{"/program": "MZ\x90\x00not an elf"})
	resolver := NewResolver(registryWith(nil), system).WithBinaries(factory)
	if _, err := resolver.Resolve(context.Background(), "/program"); !errors.Is(err, errno.NoExec) {
		t.Fatalf("err=%v want ENOEXEC", err)
	}
	if len(factory.paths) != 0 {
		t.Fatalf("不该把非映像交给执行器: %v", factory.paths)
	}
}

func TestBinaryImageContentIsBound(t *testing.T) {
	content := elfImage()
	system := newScriptFS(t, map[string]string{"/program": content})
	image := &memoryImage{digests: map[string]string{"/program": digest.Content([]byte("another image"))}}
	factory := &binaryFactory{program: Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil })}
	resolver := NewBoundResolver(registryWith(nil), system, image).WithBinaries(factory)
	if _, err := resolver.Resolve(context.Background(), "/program"); !errors.Is(err, errno.NoExec) {
		t.Fatalf("内容不符: err=%v want ENOEXEC", err)
	}

	image.digests["/program"] = digest.Content([]byte(content))
	resolved, err := resolver.Resolve(context.Background(), "/program")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ABI != ABIELF || resolved.ContentDigest != digest.Content([]byte(content)) {
		t.Fatalf("解析=%+v", resolved)
	}
}
