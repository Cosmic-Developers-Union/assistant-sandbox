package guest_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

// h13BgDigest 锁定后台进程探针摘要(重建须显式更新)。源码要点:
// 父进程 fork 后立即 _exit(0);子进程写标记 1 → 睡 300ms → 追加
// 标记 2 → 以 7 退出。工具链 zig 0.16.0:
// `zig cc -dynamic -target riscv64-linux-musl -O2 -s -o h13-bg h13bg.c`。
const (
	h13BgDigest = "1115012d84e96a038ca551530936e606eec78346df8736b1bd0b466bc617244d"
)

// TestBackgroundChildOutlivesCallerOnVM 验证 H13:调用方(父进程)
// 等待返回后,客体子进程仍运行并继续写文件;子进程退出后可查询
// 并由 init 回收——Shell 无关,纯内核语义。
func TestBackgroundChildOutlivesCallerOnVM(t *testing.T) {
	image, err := os.ReadFile("testdata/h13-bg")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(image)
	if got := hex.EncodeToString(sum[:]); got != h13BgDigest {
		t.Fatalf("后台进程探针摘要=%s want %s(产物与锁定不一致,先复核工具链再更新)", got, h13BgDigest)
	}
	kernel, system := guestKernel(t, image)
	defer func() { _ = kernel.Close(context.Background()) }()
	// 装配动态装载环境(musl)。
	if err := system.Mkdir(context.Background(), "/lib", 0o755); err != nil {
		t.Fatal(err)
	}
	loader := f10Fixture(t, "lib/ld-musl-riscv64.so.1", f10LoaderDigest)
	for _, p := range []string{"/lib/ld-musl-riscv64.so.1", "/lib/libc.so"} {
		file, err := system.Open(context.Background(), p, vmfs.Create|vmfs.ReadWrite, 0o755)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(loader); err != nil {
			t.Fatal(err)
		}
		_ = file.Close()
	}

	// 经 memfs 建好工作区后由父进程运行。先手动把探针写入 /probe,
	// 由 guestKernel 已写入(t.Helper 内)。运行父进程:
	output := &strings.Builder{}
	parent, err := kernel.Begin(0, "/probe", []string{"/probe"}, "/", nil, fd.Streams{Output: output})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.RunRegistered(context.Background(), parent); err != nil {
		t.Fatalf("父进程运行: %v", err)
	}
	// 父进程已返回:读取子进程 PID(探针写入 /h13.pid)。
	pidFile, err := system.Open(context.Background(), "/h13.pid", 0, 0)
	if err != nil {
		t.Fatalf("父进程应已写出子进程 PID: %v", err)
	}
	raw := make([]byte, 32)
	n, _ := pidFile.Read(raw)
	_ = pidFile.Close()
	childPID64, err := strconv.ParseUint(strings.TrimSpace(string(raw[:n])), 10, 32)
	if err != nil {
		t.Fatalf("子进程 PID 解析: %v", err)
	}
	childPID := proc.PID(childPID64)

	// 子进程仍运行(父进程退出后被 init 收养)。
	live := kernel.Processes()
	alive := false
	for _, p := range live {
		if p.ID == childPID {
			alive = true
		}
	}
	if !alive {
		t.Fatalf("子进程应仍在运行: %+v", live)
	}

	// 子进程继续写文件:轮询 /h13.log 直到包含 stage2(第二段写入)。
	deadline := time.Now().Add(5 * time.Second)
	var content string
	for {
		log, err := system.Open(context.Background(), "/h13.log", 0, 0)
		if err == nil {
			data := make([]byte, 64)
			readN, _ := log.Read(data)
			_ = log.Close()
			content = string(data[:readN])
			if strings.Contains(content, "stage2") {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("子进程未完成写入: %q", content)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(content, "stage1+stage2") {
		t.Fatalf("写入内容=%q want stage1+stage2", content)
	}

	// 子进程退出(7)后由 init 回收:查询并回收。
	deadline = time.Now().Add(5 * time.Second)
	var reaped proc.ChildState
	for {
		state, found, waitErr := kernel.WaitChild(context.Background(), 0, childPID, true)
		if waitErr != nil {
			t.Fatalf("init 回收: %v", waitErr)
		}
		if found {
			reaped = state
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("子进程僵尸未被回收")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if reaped.ExitCode != 7 {
		t.Fatalf("子进程退出码=%d want 7", reaped.ExitCode)
	}
}
