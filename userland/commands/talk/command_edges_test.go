package talk_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	stdfs "io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	talkcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/talk"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

const channelPath = "/var/spool/talk/channel"

func channelHostPath(dir string) string {
	return filepath.Join(dir, strings.TrimPrefix(channelPath, "/"))
}

func readChannelText(t *testing.T, dir string) string {
	t.Helper()
	file, err := rootFSFor(dir).Open(context.Background(), channelPath, vmfs.ReadOnly, 0)
	if err != nil {
		t.Fatalf("打开通道: %v", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("读取通道: %v", err)
	}
	return string(data)
}

func assertChannel(t *testing.T, dir, want string) {
	t.Helper()
	if got := readChannelText(t, dir); got != want {
		t.Fatalf("通道内容 = %q，期望 %q", got, want)
	}
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	status, ok := errors.AsType[interp.ExitStatus](err)
	if !ok {
		t.Fatalf("非退出码错误: %v", err)
	}
	return int(status)
}

func invokeWithSet(t *testing.T, set capability.Set) error {
	t.Helper()
	dir := t.TempDir()
	process := proc.Proc{ID: 1, Dir: dir, Resources: set, SID: 1, PGID: 1}
	return invokeCommand(proc.WithContext(t.Context(), process), dir, nil, strings.NewReader(""))
}

func TestParseErrorRejected(t *testing.T) {
	r := commandtest.Run(t, talkcmd.Run, t.TempDir(), []string{"--nope"}, "")
	if r.Code != 2 || !strings.Contains(r.Stderr, "参数错误") {
		t.Fatalf("未知选项 = %+v", r)
	}
}

func TestMissingProcessContextFailsClosed(t *testing.T) {
	err := invokeCommand(t.Context(), t.TempDir(), nil, strings.NewReader(""))
	if code := exitCode(t, err); code != 2 {
		t.Fatalf("缺进程视图退出码 = %d (%v)", code, err)
	}
}

func TestMissingDescriptorTableFailsClosed(t *testing.T) {
	err := invokeWithSet(t, capability.Set{Clock: &clock.Virtual{}})
	if code := exitCode(t, err); code != 2 {
		t.Fatalf("缺描述符表退出码 = %d (%v)", code, err)
	}
}

func TestMissingFilesystemFailsClosed(t *testing.T) {
	f := newFixture(t)
	f.dropSystem = true
	done := f.start()
	seen := f.awaitText("talk:")
	if code := exitCode(t, f.waitDone(done)); code != 2 {
		t.Fatalf("缺文件系统退出码 = %d；输出=%q", code, seen)
	}
	if strings.Contains(seen, "等待对端") {
		t.Fatalf("能力缺失不得进入对话循环: %q", seen)
	}
}

func TestMissingStandardInputDescriptorRejected(t *testing.T) {
	f := newFixture(t)
	f.fds = []uint32{1, 2}
	done := f.start()
	seen := f.awaitText("不是终端")
	if code := exitCode(t, f.waitDone(done)); code != 1 {
		t.Fatalf("缺 fd0 退出码 = %d；输出=%q", code, seen)
	}
}

func TestSessionCancelExitsCleanly(t *testing.T) {
	f := newFixture(t)
	done := f.run()
	f.cancel()
	if err := f.waitDone(done); err != nil {
		t.Fatalf("取消会话应按正常退出处置: %v", err)
	}
}

func TestLocalReadHandleDisappearsStopsSpeech(t *testing.T) {
	f := newFixture(t)
	gone := &atomic.Bool{}
	failed := &atomic.Int64{}
	f.fds = []uint32{1, 2}
	f.stdin = vanishAfterRead{Stream: &scriptedInput{chunks: []string{"hello\n"}}, gone: gone}
	f.wrapFD = func(table fd.Descriptors) fd.Descriptors {
		return vanishingDescriptors{Descriptors: table, gone: gone, failed: failed}
	}
	done := f.run()
	f.until(func() bool { return failed.Load() > 0 })
	f.cancel()
	if err := f.waitDone(done); err != nil {
		t.Fatalf("取消会话应按正常退出处置: %v", err)
	}

	seen := f.drained + string(f.terminal.Drain())
	if strings.Contains(seen, "[me]") {
		t.Fatalf("句柄消失后不得把输入当发言: %q", seen)
	}
	if _, err := os.Stat(channelHostPath(f.dir)); !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatalf("读取失败不得落盘通道: %v", err)
	}
}

func TestInputHandleDisappearsAfterBlankLine(t *testing.T) {
	f := newFixture(t)
	gone := &atomic.Bool{}
	f.fds = []uint32{1, 2}

	f.stdin = blankThenVanish{Stream: f.terminal, gone: gone}
	f.wrapFD = func(table fd.Descriptors) fd.Descriptors {
		return vanishingDescriptors{Descriptors: table, gone: gone, failed: &atomic.Int64{}}
	}
	done := f.run()
	if _, err := f.terminal.Input([]byte{0x04}); err != nil {
		t.Error(err)
	}
	if err := f.waitDone(done); err != nil {
		t.Fatalf("空行提交后句柄消失应按 EOF 退出: %v", err)
	}
}

func TestPartialLineBufferedUntilNewline(t *testing.T) {
	f := newFixture(t)
	f.fds = []uint32{1, 2}

	f.stdin = &scriptedInput{chunks: []string{"par", "tial\n", ""}}
	done := f.run()
	seen := f.awaitText("[me] partial")
	if strings.Contains(seen, "[me] par\n") || strings.Contains(seen, "[me] tial\n") {
		t.Fatalf("半行不得被当成发言: %q", seen)
	}
	if err := f.waitDone(done); err != nil {
		t.Fatalf("脚本耗尽应按 EOF 退出: %v", err)
	}
	assertChannel(t, f.dir, "partial\n")
}

func TestEmptyChannelTreatedAsNoHistory(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(filepath.Dir(channelHostPath(f.dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(channelHostPath(f.dir), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	done := f.run()
	if _, err := f.terminal.Input([]byte("hello\n")); err != nil {
		t.Error(err)
	}
	seen := f.awaitText("[me] hello")
	if _, err := f.terminal.Input([]byte{0x04}); err != nil {
		t.Error(err)
	}
	if err := f.waitDone(done); err != nil {
		t.Fatalf("退出: %v", err)
	}
	seen += string(f.terminal.Drain())

	if got := strings.Count(seen, "[peer]"); got != 0 {
		t.Fatalf("空通道回放了历史行或自己的发言: %q", seen)
	}
	assertChannel(t, f.dir, "hello\n")
}

func TestTalkWithoutClockCapability(t *testing.T) {
	f := newFixture(t)
	f.Clock = nil
	f.run()
	if err := f.exchange("hello"); err != nil {
		t.Fatalf("无时钟能力应仍能对话: %v", err)
	}
	assertChannel(t, f.dir, "hello\n")
}

func TestTalkWithClosedClock(t *testing.T) {
	f := newFixture(t)
	closed := &clock.Virtual{}
	closed.Close()
	f.Clock = closed
	f.run()
	if err := f.exchange("hello"); err != nil {
		t.Fatalf("时钟关闭应仍能对话: %v", err)
	}
	assertChannel(t, f.dir, "hello\n")
}

func TestChannelRotationDropsOldest(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(filepath.Dir(channelHostPath(f.dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	lines := make([]string, 256)
	for i := range lines {
		lines[i] = fmt.Sprintf("old-%03d", i)
	}
	if err := os.WriteFile(channelHostPath(f.dir), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := f.run()
	if _, err := f.terminal.Input([]byte("newest\n")); err != nil {
		t.Error(err)
	}
	f.awaitText("[me] newest")
	got := strings.Split(strings.TrimSuffix(readChannelText(t, f.dir), "\n"), "\n")
	if len(got) != 256 {
		t.Fatalf("轮转后应保持 256 行: %d", len(got))
	}
	if got[0] != "old-001" || got[len(got)-1] != "newest" {
		t.Fatalf("应丢最老、留最新: 首行=%q 末行=%q", got[0], got[len(got)-1])
	}
	if _, err := f.terminal.Input([]byte{0x04}); err != nil {
		t.Error(err)
	}
	if err := f.waitDone(done); err != nil {
		t.Fatalf("退出: %v", err)
	}
}

func TestChannelDirectoryCreatedConcurrently(t *testing.T) {
	f := newFixture(t)
	f.system = mkdirRaceSystem{System: rootFSFor(f.dir), target: "/var/spool"}
	f.run()
	if err := f.exchange("hello"); err != nil {
		t.Fatalf("并发创建目录应被接受: %v", err)
	}
	assertChannel(t, f.dir, "hello\n")
}

func TestChannelDirectoryCreationFails(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.dir, "var"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := f.run()
	if _, err := f.terminal.Input([]byte("hello\n")); err != nil {
		t.Error(err)
	}
	seen := f.awaitText("talk:")
	if code := exitCode(t, f.waitDone(done)); code != 1 {
		t.Fatalf("目录创建失败退出码 = %d；输出=%q", code, seen)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "var", "spool", "talk")); err == nil {
		t.Fatalf("失败后不得留下通道目录")
	}
}

func TestChannelPathOccupiedByDirectory(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(channelHostPath(f.dir), 0o755); err != nil {
		t.Fatal(err)
	}
	done := f.run()
	if _, err := f.terminal.Input([]byte("hello\n")); err != nil {
		t.Error(err)
	}
	seen := f.awaitText("talk:")
	if code := exitCode(t, f.waitDone(done)); code != 1 {
		t.Fatalf("落点被目录占用退出码 = %d；输出=%q", code, seen)
	}
	info, err := os.Stat(channelHostPath(f.dir))
	if err != nil || !info.IsDir() {
		t.Fatalf("落点目录不应被改名替换: %v %v", info, err)
	}
}

func TestChannelTempPathOccupiedByDirectory(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(channelHostPath(f.dir)+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	done := f.run()
	if _, err := f.terminal.Input([]byte("hello\n")); err != nil {
		t.Error(err)
	}
	seen := f.awaitText("talk:")
	if code := exitCode(t, f.waitDone(done)); code != 1 {
		t.Fatalf("临时落点被目录占用退出码 = %d；输出=%q", code, seen)
	}
	if _, err := os.Stat(channelHostPath(f.dir)); !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatalf("失败不得留下半个通道文件: %v", err)
	}
}

func TestAppendWriteFaultReported(t *testing.T) {
	f := newFixture(t)
	f.system = faultSystem{System: rootFSFor(f.dir), operation: "Write"}
	done := f.run()
	if _, err := f.terminal.Input([]byte("hello\n")); err != nil {
		t.Error(err)
	}
	seen := f.awaitText("talk:")
	if code := exitCode(t, f.waitDone(done)); code != 1 {
		t.Fatalf("写失败退出码 = %d；输出=%q", code, seen)
	}
	if _, err := os.Stat(channelHostPath(f.dir)); !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatalf("写失败不得留下通道文件: %v", err)
	}
}

func TestAppendCloseFaultReported(t *testing.T) {
	f := newFixture(t)
	f.system = faultSystem{System: rootFSFor(f.dir), operation: "Close"}
	done := f.run()
	if _, err := f.terminal.Input([]byte("hello\n")); err != nil {
		t.Error(err)
	}
	seen := f.awaitText("talk:")
	if code := exitCode(t, f.waitDone(done)); code != 1 {
		t.Fatalf("关闭失败退出码 = %d；输出=%q", code, seen)
	}
	if _, err := os.Stat(channelHostPath(f.dir)); !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatalf("关闭失败不得让半成品占据落点: %v", err)
	}
}

func TestPeerLocalEchoLinesSkipped(t *testing.T) {
	f := newFixture(t)
	done := f.run()
	if err := os.MkdirAll(filepath.Dir(channelHostPath(f.dir)), 0o755); err != nil {
		t.Fatal(err)
	}

	incoming := channelHostPath(f.dir) + ".incoming"
	if err := os.WriteFile(incoming, []byte("[me] ghost\nreal peer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(incoming, channelHostPath(f.dir)); err != nil {
		t.Fatal(err)
	}
	seen := f.awaitText("[peer] real peer")
	if strings.Contains(seen, "[peer] [me] ghost") {
		t.Fatalf("本地回声不应回显成对端消息: %q", seen)
	}
	if _, err := f.terminal.Input([]byte{0x04}); err != nil {
		t.Error(err)
	}
	if err := f.waitDone(done); err != nil {
		t.Fatalf("退出: %v", err)
	}
}

func TestDeclaredIdentityRejectsOtherName(t *testing.T) {
	r := commandtest.RunWithIdentity(t, talkcmd.Run, t.TempDir(), []string{"alice"}, "", nil, identity.SingleUser{})
	if r.Code != 1 || !strings.Contains(r.Stderr, "没有这个用户") {
		t.Fatalf("身份不符 = %+v", r)
	}
}

func TestIdentityLookupFailureReported(t *testing.T) {
	r := commandtest.RunWithIdentity(t, talkcmd.Run, t.TempDir(), []string{"sandbox"}, "", nil, failingIdentity{})
	if r.Code != 1 || !strings.Contains(r.Stderr, "talk:") || !strings.Contains(r.Stderr, errno.NoProcess.Error()) {
		t.Fatalf("身份故障 = %+v", r)
	}
}

type failingIdentity struct{}

func (failingIdentity) Lookup(context.Context, uint64) (identity.Identity, error) {
	return identity.Identity{}, errno.NoProcess
}

type vanishingDescriptors struct {
	fd.Descriptors
	gone   *atomic.Bool
	failed *atomic.Int64
}

func (v vanishingDescriptors) Get(ctx context.Context, pid uint64, id uint32) (fd.Stream, error) {
	if id == 0 && v.gone.Load() {
		v.failed.Add(1)
		return nil, errno.NoDevice
	}
	return v.Descriptors.Get(ctx, pid, id)
}

type blankThenVanish struct {
	fd.Stream
	gone *atomic.Bool
}

type vanishAfterRead struct {
	fd.Stream
	gone *atomic.Bool
}

func (v vanishAfterRead) Read(p []byte) (int, error) {
	n, err := v.Stream.Read(p)
	if n > 0 {
		v.gone.Store(true)
	}
	return n, err
}

func (b blankThenVanish) Read(p []byte) (int, error) {
	n, err := b.Stream.Read(p)
	if n == 0 && err == nil {
		b.gone.Store(true)
	}
	return n, err
}

type scriptedInput struct {
	chunks []string
}

func (s *scriptedInput) Read(p []byte) (int, error) {
	if len(s.chunks) == 0 {
		return 0, io.EOF
	}
	chunk := s.chunks[0]
	s.chunks = s.chunks[1:]
	if len(chunk) > len(p) {

		s.chunks = append([]string{chunk[len(p):]}, s.chunks...)
		chunk = chunk[:len(p)]
	}
	return copy(p, chunk), nil
}

func (s *scriptedInput) Write(p []byte) (int, error) { return len(p), nil }
func (s *scriptedInput) Close() error                { return nil }

type mkdirRaceSystem struct {
	vmfs.System
	target string
}

func (m mkdirRaceSystem) Mkdir(ctx context.Context, name string, mode stdfs.FileMode) error {
	if name != m.target {
		return m.System.Mkdir(ctx, name, mode)
	}
	if err := m.System.Mkdir(ctx, name, mode); err != nil {
		return err
	}
	return stdfs.ErrExist
}

type faultSystem struct {
	vmfs.System
	operation string
}

func (f faultSystem) Open(ctx context.Context, name string, flags uint32, mode stdfs.FileMode) (vmfs.File, error) {
	file, err := f.System.Open(ctx, name, flags, mode)
	if err != nil {
		return nil, err
	}
	return faultFile{File: file, operation: f.operation}, nil
}

type faultFile struct {
	vmfs.File
	operation string
}

func (f faultFile) Write(p []byte) (int, error) {
	if f.operation == "Write" {
		return 0, io.ErrClosedPipe
	}
	return f.File.Write(p)
}

func (f faultFile) Close() error {
	err := f.File.Close()
	if f.operation == "Close" {
		return io.ErrClosedPipe
	}
	return err
}
