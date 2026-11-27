package guest_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

// compatImageRootfs 把外部 rootfs 归档装入内存根文件系统,再生成可
// 复用的镜像。init 必须在 VM 创建前就位于根文件系统中,因此不能
// 使用先创建 VM、后解包的 compatRootfsGuest。
func compatImageRootfs(t *testing.T, archivePath string, mutate func(vmfs.System)) *instance.Image {
	t.Helper()
	root, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"/work", "/tmp", "/root"} {
		if err := root.Mkdir(t.Context(), dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	files := tar.NewReader(reader)
	for {
		header, err := files.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		name := path.Clean(strings.TrimPrefix(header.Name, "./"))
		target := "/" + name
		switch header.Typeflag {
		case tar.TypeDir:
			if err := root.Mkdir(t.Context(), target, 0o755); err != nil {
				if info, statErr := root.Stat(t.Context(), target); statErr != nil || !info.IsDir() {
					t.Fatal(err)
				}
			}
		case tar.TypeReg:
			data, err := io.ReadAll(io.LimitReader(files, 64<<20))
			if err != nil {
				t.Fatal(err)
			}
			mode := header.FileInfo().Mode().Perm()
			if mode == 0 {
				mode = 0o644
			}
			writeRootFile(t, root, target, data, mode)
		case tar.TypeSymlink:
			if err := root.Symlink(t.Context(), header.Linkname, target); err != nil {
				if _, statErr := root.Lstat(t.Context(), target); statErr != nil {
					t.Fatal(err)
				}
			}
		case tar.TypeLink:
			source := "/" + path.Clean(strings.TrimPrefix(header.Linkname, "./"))
			if err := root.Link(t.Context(), source, target); err != nil {
				if _, statErr := root.Lstat(t.Context(), target); statErr != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if mutate != nil {
		mutate(root)
	}
	image, err := instance.NewImageFrom(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	return image
}

func writeRootFile(t *testing.T, root vmfs.System, name string, data []byte, mode os.FileMode) {
	t.Helper()
	file, err := root.Open(t.Context(), name, vmfs.Create|vmfs.Truncate|vmfs.WriteOnly, mode)
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

// TestCompatibilityS4ExternalInit 用外部发行版 init 作为 PID 1 启动:
// 验证 inittab 的 sysinit/respawn、/dev/console 与 /dev/tty1、namespace
// 以及 cgroup 视图。控制用 inittab 让断言不依赖镜像是否附带 openrc。
func TestCompatibilityS4ExternalInit(t *testing.T) {
	image := os.Getenv("COMPAT_S4_IMAGE")
	if image == "" {
		t.Skip("设置 COMPAT_S4_IMAGE 后运行外部发行版 init 门禁")
	}
	rootfs := compatImageRootfs(t, image, func(system vmfs.System) {
		writeRootFile(t, system, "/work/init-sysinit.sh", []byte(`#!/bin/sh
set -eu
printf 'sysinit-ok\n' > /work/init-sysinit
exec 3<>/dev/console
printf 'console-open-ok\n' >> /work/init-sysinit
exec 4<>/dev/tty1
printf 'tty-open-ok\n' >> /work/init-sysinit
`), 0o755)
		writeRootFile(t, system, "/etc/inittab", []byte(
			"::sysinit:/work/init-sysinit.sh\n"+
				"::respawn:/bin/sleep 300\n"), 0o644)
	})
	output := &lockedBuffer{}
	machine, err := rootfs.Instantiate(t.Context(), instance.Options{
		Deterministic: false,
		Seed:          1,
		MemoryBytes:   2 << 30,
		Session:       resources.Limits{Memory: 2 << 30, Work: 1 << 38},
		Process:       resources.Limits{Memory: 1 << 30, Work: 1 << 38},
		Init: &instance.InitSpec{
			Program: "/sbin/init",
			Dir:     "/",
			Env:     map[string]string{"PATH": "/sbin:/usr/sbin:/bin:/usr/bin"},
			Streams: fd.Streams{Output: output, Error: output},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = machine.Close(context.Background()) })

	deadline := time.Now().Add(30 * time.Second)
	var marker string
	for {
		marker, _ = readGuestFile(machine, "/work/init-sysinit")
		if strings.Count(marker, "-ok\n") == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("init sysinit 未完成: marker=%q output=%q", marker, output.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(marker, "sysinit-ok\n") ||
		!strings.Contains(marker, "console-open-ok\n") ||
		!strings.Contains(marker, "tty-open-ok\n") {
		t.Fatalf("init sysinit 输出=%q", marker)
	}

	initProcess := processByID(machine.Kernel().Processes(), machine.Kernel().Init())
	if initProcess.Program != "/sbin/init" || initProcess.Parent != 0 ||
		(initProcess.State != proc.Running && initProcess.State != proc.Waiting) {
		t.Fatalf("PID 1=%+v", initProcess)
	}
	cmdline, err := readGuestFile(machine, "/proc/1/cmdline")
	if err != nil || strings.TrimRight(cmdline, "\x00") != "/sbin/init" {
		t.Fatalf("PID 1 cmdline=%q err=%v", cmdline, err)
	}
	status, err := readGuestFile(machine, "/proc/1/status")
	if err != nil || !strings.Contains(status, "Pid:\t1\n") || !strings.Contains(status, "PPid:\t0\n") {
		t.Fatalf("PID 1 status=%q err=%v", status, err)
	}
	cgroup, err := readGuestFile(machine, "/proc/1/cgroup")
	if err != nil || cgroup != "0::/assistant-sandbox\n" {
		t.Fatalf("PID 1 cgroup=%q err=%v", cgroup, err)
	}

	namespaces := map[string]string{}
	for _, name := range []string{"cgroup", "ipc", "mnt", "net", "pid", "time", "user", "uts"} {
		target, err := machine.Filesystem().Readlink(t.Context(), "/proc/1/ns/"+name)
		if err != nil {
			t.Fatalf("readlink ns/%s: %v", name, err)
		}
		if !strings.HasPrefix(target, name+":[") || !strings.HasSuffix(target, "]") {
			t.Fatalf("ns/%s=%q", name, target)
		}
		namespaces[name] = target
	}
	pidChildren, err := machine.Filesystem().Readlink(t.Context(), "/proc/1/ns/pid_for_children")
	if err != nil || pidChildren != namespaces["pid"] {
		t.Fatalf("pid_for_children=%q pid=%q err=%v", pidChildren, namespaces["pid"], err)
	}

	var sleepProcess proc.Proc
	var childNamespace string
	var namespaceErr error
	deadline = time.Now().Add(30 * time.Second)
	for childNamespace == "" {
		for _, process := range machine.Kernel().Processes() {
			if process.Program == "/bin/sleep" && process.State == proc.Running {
				sleepProcess = process
				break
			}
		}
		if sleepProcess.ID != 0 {
			childNamespace, namespaceErr = machine.Filesystem().Readlink(t.Context(),
				"/proc/"+strconv.FormatUint(uint64(sleepProcess.ID), 10)+"/ns/mnt")
			if namespaceErr == nil {
				break
			}
			// init 的 respawn 子进程可能在快照与 Readlink 之间退出；
			// 重新等待下一个稳定子进程，而不是把竞态当语义失败。
			sleepProcess = proc.Proc{}
		}
		if time.Now().After(deadline) {
			t.Fatalf("读取 respawn 子进程 namespace 超时: output=%q err=%v", output.String(), namespaceErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if sleepProcess.Parent != initProcess.ID {
		t.Fatalf("respawn 父进程=%d want %d", sleepProcess.Parent, initProcess.ID)
	}
	if childNamespace != namespaces["mnt"] {
		t.Fatalf("子进程 mnt namespace=%q want %q", childNamespace, namespaces["mnt"])
	}

	procs, err := readGuestFile(machine, "/sys/fs/cgroup/assistant-sandbox/cgroup.procs")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []proc.PID{initProcess.ID, sleepProcess.ID} {
		if !strings.Contains(procs, strconv.FormatUint(uint64(id), 10)+"\n") {
			t.Fatalf("cgroup.procs=%q 缺少 %d", procs, id)
		}
	}
	pidsCurrent, err := readGuestFile(machine, "/sys/fs/cgroup/assistant-sandbox/pids.current")
	if err != nil {
		t.Fatal(err)
	}
	current, err := strconv.Atoi(strings.TrimSpace(pidsCurrent))
	if err != nil || current < 2 {
		t.Fatalf("pids.current=%q err=%v", pidsCurrent, err)
	}
	memoryMax, err := readGuestFile(machine, "/sys/fs/cgroup/assistant-sandbox/memory.max")
	if err != nil || strings.TrimSpace(memoryMax) != strconv.FormatUint(2<<30, 10) {
		t.Fatalf("memory.max=%q err=%v", memoryMax, err)
	}

	// respawn:杀掉子进程后 init 应创建新的 sleep。
	if err := machine.Kernel().Signal(sleepProcess.ID, signal.KILL); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(30 * time.Second)
	for {
		for _, process := range machine.Kernel().Processes() {
			if process.Program == "/bin/sleep" && process.State == proc.Running && process.ID != sleepProcess.ID {
				initProcess = processByID(machine.Kernel().Processes(), machine.Kernel().Init())
				if initProcess.State != proc.Running && initProcess.State != proc.Waiting {
					t.Fatalf("respawn 后 PID 1 状态=%v", initProcess.State)
				}
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("init 未 respawn 子进程: output=%q", output.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func processByID(processes []proc.Proc, id proc.PID) proc.Proc {
	for _, process := range processes {
		if process.ID == id {
			return process
		}
	}
	return proc.Proc{}
}

// TestCompatibilityS4ExternalDistroInittab 用镜像自带的 /etc/inittab
// 启动发行版 init:验证真实 sysinit/getty 路径,且 tty 打开失败不会
// 形成高频 respawn 循环。
func TestCompatibilityS4ExternalDistroInittab(t *testing.T) {
	image := os.Getenv("COMPAT_S4_IMAGE")
	if image == "" {
		t.Skip("设置 COMPAT_S4_IMAGE 后运行外部发行版 inittab 门禁")
	}
	rootfs := compatImageRootfs(t, image, nil)
	output := &lockedBuffer{}
	machine, err := rootfs.Instantiate(t.Context(), instance.Options{
		Deterministic: false,
		Seed:          1,
		MemoryBytes:   2 << 30,
		Session:       resources.Limits{Memory: 2 << 30, Work: 1 << 38},
		Process:       resources.Limits{Memory: 1 << 30, Work: 1 << 38},
		Init: &instance.InitSpec{
			Program: "/sbin/init",
			Dir:     "/",
			Env:     map[string]string{"PATH": "/sbin:/usr/sbin:/bin:/usr/bin"},
			Streams: fd.Streams{Output: output, Error: output},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = machine.Close(context.Background()) })
	deadline := time.Now().Add(30 * time.Second)
	var gettyFound bool
	processes := machine.Kernel().Processes()
	for !gettyFound {
		processes = machine.Kernel().Processes()
		for _, process := range processes {
			if process.Program == "/sbin/getty" && process.Parent == machine.Kernel().Init() &&
				process.State == proc.Running {
				gettyFound = true
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("inittab 未启动 getty: output=%q", output.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	initProcess := processByID(processes, machine.Kernel().Init())
	if initProcess.Program != "/sbin/init" ||
		(initProcess.State != proc.Running && initProcess.State != proc.Waiting) {
		t.Fatalf("PID 1=%+v", initProcess)
	}
	if strings.Contains(output.String(), "can't open /dev/tty") {
		t.Fatalf("getty 打开 tty 失败: output=%q", output.String())
	}
	if len(processes) > 64 {
		t.Fatalf("init respawn 失控: %d 个进程", len(processes))
	}
}
