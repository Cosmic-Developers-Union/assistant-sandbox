package guest_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"path"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
)

// node22RuntimeDigest 锁定 Alpine 3.22 riscv64 的 Node.js 22.23.5-r0
// 运行时归档:usr/bin/node、musl 加载器、17 个共享库与 ICU 数据。
const node22RuntimeDigest = "49c1cca5b16b0ada706083d8e50b003a3e79723d9d2e760cf3400e24442210df"

// node22Guest 启动一台装配真实 riscv64 Node.js 22.23.5 的 VM。
func node22Guest(t *testing.T) (*instance.VM, *instance.Connection) {
	t.Helper()
	return node22GuestWith(t, nil)
}

// node22GuestWith 与 node22Guest 相同,但允许测试装配宿主网络、
// 出口策略等 VM 选项(M10/M11)。
func node22GuestWith(t *testing.T, configure func(*instance.Options)) (*instance.VM, *instance.Connection) {
	t.Helper()
	archive := python312Asset(t, "testdata/nodejs/node22-runtime.tar.gz", node22RuntimeDigest)
	options := instance.Options{
		Deterministic: true,
		Seed:          1,
		Session:       resources.Limits{Memory: 4 << 30, Work: 1 << 38},
		Process:       resources.Limits{Memory: 2 << 30, Work: 1 << 38},
	}
	if configure != nil {
		configure(&options)
	}
	machine, err := instance.New(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = machine.Close(context.Background()) })
	extractNodeRuntime(t, machine, archive)
	connection, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return machine, connection
}

// extractNodeRuntime 展开 Node 运行时归档;归档已去除 uid/gid、
// mtime 与宿主路径,内容摘要由 node22RuntimeDigest 锁定。
func extractNodeRuntime(t *testing.T, machine *instance.VM, archive []byte) {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	files := tar.NewReader(reader)
	for {
		header, err := files.Next()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		name := path.Clean(header.Name)
		target := "/" + name
		switch header.Typeflag {
		case tar.TypeDir:
			_ = machine.Filesystem().Mkdir(t.Context(), target, 0o755)
		case tar.TypeReg:
			data, err := io.ReadAll(io.LimitReader(files, 64<<20))
			if err != nil {
				t.Fatal(err)
			}
			mode := header.FileInfo().Mode().Perm()
			if mode == 0 {
				mode = 0o644
			}
			writeGuestFile(t, machine, target, data, mode)
		case tar.TypeSymlink:
			_ = machine.Filesystem().Symlink(t.Context(), header.Linkname, target)
		}
	}
}

// runNode22 在客体中运行 /usr/bin/node,分别返回 stdout 与 stderr。
func runNode22(t *testing.T, connection *instance.Connection, args []string, timeout time.Duration) (string, string, uint8) {
	t.Helper()
	return runNode22Env(t, connection, args, timeout, nil)
}

// runNode22Env 在 runNode22 的基础上追加客体环境变量(M11 CA 配置)。
func runNode22Env(t *testing.T, connection *instance.Connection, args []string, timeout time.Duration, extra map[string]string) (string, string, uint8) {
	t.Helper()
	output := &bytes.Buffer{}
	diagnostic := &bytes.Buffer{}
	runCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	env := map[string]string{
		"PATH": "/usr/bin:/bin", "HOME": "/work", "LANG": "C.UTF-8",
		"NODE_ICU_DATA": "/usr/share/icu/76.1",
	}
	for name, value := range extra {
		env[name] = value
	}
	process, err := connection.Exec(runCtx, instance.ExecSpec{
		Program: "/usr/bin/node",
		Args:    args,
		Dir:     "/work",
		Env:     env,
		Streams: fd.Streams{Output: output, Error: diagnostic},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := connection.Wait(runCtx, process.ID)
	if err != nil {
		t.Fatalf("node %v: %v (stdout=%q stderr=%q)", args, err, output.String(), diagnostic.String())
	}
	return output.String(), diagnostic.String(), state.ExitCode
}
