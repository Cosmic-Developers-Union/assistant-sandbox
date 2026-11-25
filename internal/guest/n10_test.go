package guest_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

// N10 固定发行物:Alpine 3.22 riscv64 真实 ELF(Bash 动态链接,
// BusyBox 静态提供基础工具,musl/readline/ncurses 为运行库)。
var n10Assets = map[string]string{
	"bin/bash":                   "009e1bab308b4c6df742c7768ae0fa7b182b78256f92f93acdb10d516bf39a94",
	"bin/busybox.static":         "62831fb7c4a0da509481107a8aeb022244235c5dced18101e3d39131d303d704",
	"lib/ld-musl-riscv64.so.1":   "23ea7ef0c0fb58d350b0a847c75de7529a68c240af87c4fdd4a327444801b0fd",
	"usr/lib/libreadline.so.8.2": "e8cce022079187a8b353a6baa7611f44092d97bd98096d538cc5a6f81729cca2",
	"usr/lib/libncursesw.so.6.5": "28e544f2e21d8a9a0458b8f3425ee46a416c0d35d2ec1a4ae44fa60b9019c3e4",
}

// installN10 把真实客体发行物写入 VM 根文件系统并建立动态链接与
// BusyBox applet 符号链接。
func installN10(t *testing.T, machine *instance.VM) {
	t.Helper()
	ctx := context.Background()
	for name, want := range n10Assets {
		data, err := os.ReadFile("testdata/n10/" + name)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Fatalf("%s 摘要=%s want %s", name, got, want)
		}
		file, err := machine.Filesystem().Open(ctx, "/"+name, vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, 0o755)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, link := range [][2]string{
		{"/lib/libc.musl-riscv64.so.1", "ld-musl-riscv64.so.1"},
		{"/usr/lib/libreadline.so.8", "libreadline.so.8.2"},
		{"/usr/lib/libncursesw.so.6", "libncursesw.so.6.5"},
		{"/bin/sh", "bash"},
	} {
		if err := machine.Filesystem().Symlink(ctx, link[1], link[0]); err != nil && !errors.Is(err, errno.Exist) {
			t.Fatal(err)
		}
	}
	for _, applet := range []string{
		"cat", "cp", "echo", "find", "grep", "kill", "ls", "mkdir", "ps",
		"sed", "sleep", "wc", "xargs",
	} {
		if err := machine.Filesystem().Symlink(ctx, "busybox.static", "/bin/"+applet); err != nil && !errors.Is(err, errno.Exist) {
			t.Fatal(err)
		}
	}
}

// TestRealBashAndToolsRunOnVM 验证 N10:从客体 ELF 执行真实 Bash,
// 由真实 ELF 工具完成文件、文本、管道与进程命令,不使用 Go 内建命令。
func TestRealBashAndToolsRunOnVM(t *testing.T) {
	machine, err := instance.New(t.Context(), instance.Options{Deterministic: true, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = machine.Close(context.Background()) }()
	installN10(t, machine)
	connection, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	script := `set -e
echo hello > /work/a.txt
cat /work/a.txt
mkdir -p /work/dir
cp /work/a.txt /work/dir/b.txt
sed 's/hello/world/' /work/dir/b.txt > /work/world.txt
grep -q world /work/world.txt
find /work/dir -name b.txt | xargs grep -q hello
echo pipeline-ok | wc -l
sh -c 'exit 7' || test $? -eq 7
(echo background > /work/bg.txt) &
wait
test -s /work/bg.txt
ps | grep -q bash
printf 'N10-OK\n'`
	output := &bytes.Buffer{}
	diagnostic := &bytes.Buffer{}
	process, err := connection.Exec(t.Context(), instance.ExecSpec{
		Program: "/bin/bash",
		Args:    []string{"-c", script},
		Dir:     "/work",
		Env: map[string]string{
			"HOME": "/work", "PATH": "/usr/local/bin:/usr/bin:/bin", "LANG": "C.UTF-8",
		},
		Streams: fd.Streams{Output: output, Error: diagnostic},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := connection.Wait(t.Context(), process.ID)
	if err != nil {
		t.Fatal(err)
	}

	if state.ExitCode != 0 || state.ExitSignal != 0 {
		for _, entry := range machine.Kernel().Trace().Entries() {
			if entry.Number == 63 || entry.Number == 64 || entry.Number == 59 || entry.Number == 57 {
				t.Logf("trace pid=%d call=%d result=%d", entry.PID, entry.Number, entry.Result)
			}
		}
		t.Fatalf("Bash 退出=%+v stdout=%q stderr=%q", state, output.String(), diagnostic.String())
	}
	if output.String() != "hello\n1\nN10-OK\n" {
		t.Fatalf("stdout=%q stderr=%q", output.String(), diagnostic.String())
	}
}
