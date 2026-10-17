package vm

import (
	"bytes"
	"context"
	"errors"
	stdfs "io/fs"
	"math"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

type jobsFault struct {
	t      *testing.T
	Kernel *Kernel
	Clock  *clock.Virtual
	FS     *memfs.FS

	runs []string
}

func newJobsFault(t *testing.T, handle func(context.Context, proc.Proc, string) (uint8, error), tweak func(*Services)) *jobsFault {
	t.Helper()
	fixture := &jobsFault{t: t}
	clk := &clock.Virtual{}
	filesystem, err := memfs.New(clk, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]executable.Program{}
	if handle != nil {
		entries["bash"] = executable.Func(func(ctx context.Context, p proc.Proc) (uint8, error) {
			script := ""
			if len(p.Args) >= 2 && p.Args[0] == "-c" {
				script = p.Args[1]
			}
			fixture.runs = append(fixture.runs, script)
			return handle(ctx, p, script)
		})
	}
	registry, err := executable.New(entries)
	if err != nil {
		t.Fatal(err)
	}
	services := Services{
		Set:      capability.Set{FS: filesystem, FD: fd.New(), Clock: clk},
		Programs: registry,
	}
	if tweak != nil {
		tweak(&services)
	}
	kernel, err := New(services, 16)
	if err != nil {
		t.Fatal(err)
	}
	fixture.Kernel = kernel
	fixture.Clock = clk
	fixture.FS = filesystem
	return fixture
}

func (f *jobsFault) injectFault(fault *faultSystem) { f.Kernel.services.FS = fault }

func writeStdout(ctx context.Context, p proc.Proc, text string) error {
	descriptors, err := p.Resources.Descriptors()
	if err != nil {
		return err
	}
	stream, err := descriptors.Get(ctx, uint64(p.ID), 1)
	if err != nil {
		return err
	}
	_, err = stream.Write([]byte(text))
	return err
}

func guestWrite(t *testing.T, system vmfs.System, name, data string) {
	t.Helper()
	ctx := context.Background()
	if err := mkdirAll(ctx, system, path.Dir(name)); err != nil {
		t.Fatalf("建目录 %s: %v", path.Dir(name), err)
	}
	file, err := system.Open(ctx, name, vmfs.WriteOnly|vmfs.Create|vmfs.Exclusive, 0o600)
	if err != nil {
		t.Fatalf("建文件 %s: %v", name, err)
	}
	if _, err := file.Write([]byte(data)); err != nil {
		t.Fatalf("写 %s: %v", name, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("关闭 %s: %v", name, err)
	}
}

func guestRead(t *testing.T, system vmfs.System, name string) (string, bool) {
	t.Helper()
	file, err := system.Open(context.Background(), name, vmfs.ReadOnly, 0)
	if err != nil {
		if errors.Is(err, errno.NotFound) || errors.Is(err, stdfs.ErrNotExist) {
			return "", false
		}
		t.Fatalf("打开 %s: %v", name, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	var buffer bytes.Buffer
	if _, err := buffer.ReadFrom(file); err != nil {
		t.Fatalf("读 %s: %v", name, err)
	}
	return buffer.String(), true
}

type faultSystem struct {
	vmfs.System
	statErr    map[string]error
	readDirErr map[string]error
	mkdirErr   map[string]error
	removeErr  map[string]error
	openErr    map[string]error

	openAfter map[string]int
	readErr   map[string]error
	writeErr  map[string]error
	closeErr  map[string]error
}

func (f *faultSystem) Stat(ctx context.Context, name string) (stdfs.FileInfo, error) {
	if err := f.statErr[name]; err != nil {
		return nil, err
	}
	return f.System.Stat(ctx, name)
}

func (f *faultSystem) ReadDir(ctx context.Context, name string) ([]stdfs.DirEntry, error) {
	if err := f.readDirErr[name]; err != nil {
		return nil, err
	}
	return f.System.ReadDir(ctx, name)
}

func (f *faultSystem) Mkdir(ctx context.Context, name string, mode stdfs.FileMode) error {
	if err := f.mkdirErr[name]; err != nil {
		return err
	}
	return f.System.Mkdir(ctx, name, mode)
}

func (f *faultSystem) Remove(ctx context.Context, name string) error {
	if err := f.removeErr[name]; err != nil {
		return err
	}
	return f.System.Remove(ctx, name)
}

func (f *faultSystem) Open(ctx context.Context, name string, flags uint32, mode stdfs.FileMode) (vmfs.File, error) {
	if err := f.openErr[name]; err != nil {
		return nil, err
	}
	if limit, ok := f.openAfter[name]; ok {
		if limit <= 0 {
			return nil, errno.IO
		}
		f.openAfter[name] = limit - 1
	}
	file, err := f.System.Open(ctx, name, flags, mode)
	if err != nil {
		return nil, err
	}
	wrapped := &faultFile{File: file, readErr: f.readErr[name], writeErr: f.writeErr[name], closeErr: f.closeErr[name]}
	if wrapped.readErr == nil && wrapped.writeErr == nil && wrapped.closeErr == nil {
		return file, nil
	}
	return wrapped, nil
}

type faultFile struct {
	vmfs.File
	readErr  error
	writeErr error
	closeErr error
}

func (f *faultFile) Read(p []byte) (int, error) {
	if f.readErr != nil {
		return 0, f.readErr
	}
	return f.File.Read(p)
}

func (f *faultFile) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.File.Write(p)
}

func (f *faultFile) Close() error {
	if f.closeErr != nil {
		return f.closeErr
	}
	return f.File.Close()
}

type declaredIdentity struct{ name string }

func (d declaredIdentity) Lookup(context.Context, uint64) (identity.Identity, error) {
	return identity.Identity{Name: d.name}, nil
}

func TestJobSpoolRequiresFilesystemCapability(t *testing.T) {
	ctx := context.Background()
	f := newJobsFault(t, nil, func(services *Services) { services.FS = nil })
	if _, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'a', Dir: "/", Script: "x"}); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("QueueJob: %v", err)
	}
	if _, err := f.Kernel.ListJobs(ctx); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("ListJobs: %v", err)
	}
	if _, err := f.Kernel.RemoveJob(ctx, "a0001"); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("RemoveJob: %v", err)
	}
	if _, err := f.Kernel.ReadJob(ctx, "a0001"); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("ReadJob: %v", err)
	}
	if _, err := f.Kernel.QueuePrint(ctx, proc.PrintSpec{Destination: "lp0", Copies: 1, Copy: true}); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("QueuePrint: %v", err)
	}
	if _, err := f.Kernel.ListPrintRequests(ctx); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("ListPrintRequests: %v", err)
	}
	if err := f.Kernel.RunDueJobs(ctx); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("RunDueJobs: %v", err)
	}
	if err := f.Kernel.logNotice(ctx, "jobs", "诊断"); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("logNotice: %v", err)
	}
	if err := f.Kernel.deliverMail(ctx, "主题", "正文"); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("deliverMail: %v", err)
	}

	if _, err := f.Kernel.QueuePrint(ctx, proc.PrintSpec{Destination: "nope", Copies: 1}); !errors.Is(err, errno.NotFound) {
		t.Fatalf("未知目的地应报 ENOENT: %v", err)
	}
}

func TestJobSpoolBackendFailuresAreReported(t *testing.T) {
	ctx := context.Background()
	f := newJobsFault(t, nil, nil)
	fault := &faultSystem{
		System:     f.FS,
		readDirErr: map[string]error{AtSpoolDir: errno.IO},
		removeErr:  map[string]error{AtSpoolDir + "/a0001": errno.IO},
	}
	f.injectFault(fault)
	if _, err := f.Kernel.ListJobs(ctx); !errors.Is(err, errno.IO) {
		t.Fatalf("目录读不到应报 EIO: %v", err)
	}

	if _, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'a', Dir: "/", Script: "x"}); !errors.Is(err, errno.IO) {
		t.Fatalf("序号探测失败应报 EIO: %v", err)
	}
	if _, err := f.Kernel.RemoveJob(ctx, "a0001"); !errors.Is(err, errno.IO) {
		t.Fatalf("删除失败应报 EIO: %v", err)
	}
	if err := f.Kernel.RunDueJobs(ctx); !errors.Is(err, errno.IO) {
		t.Fatalf("检查点的 spool 扫描失败应报 EIO: %v", err)
	}

	fault.readDirErr = nil
	fault.statErr = map[string]error{AtSpoolDir: errno.IO}
	if _, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'a', Dir: "/", Script: "x"}); !errors.Is(err, errno.IO) {
		t.Fatalf("目录探测失败应报 EIO: %v", err)
	}

	fault.statErr = map[string]error{path.Dir(PrintRequestDir): errno.IO}
	if _, err := f.Kernel.QueuePrint(ctx, proc.PrintSpec{Destination: "lp0", Copies: 1, Copy: true}); !errors.Is(err, errno.IO) {
		t.Fatalf("父目录探测失败应报 EIO: %v", err)
	}

	fault.statErr = nil
	fault.openErr = map[string]error{AtSpoolDir + "/a0001": errno.IO}
	if _, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'a', Dir: "/", Script: "x"}); !errors.Is(err, errno.IO) {
		t.Fatalf("打开 spool 文件失败应报 EIO: %v", err)
	}
	fault.openErr = nil
	fault.writeErr = map[string]error{AtSpoolDir + "/a0001": errno.IO}
	if _, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'a', Dir: "/", Script: "x"}); !errors.Is(err, errno.IO) {
		t.Fatalf("写 spool 文件失败应报 EIO: %v", err)
	}
}

func TestJobClaimAndRunFailuresAreReported(t *testing.T) {
	ctx := context.Background()
	f := newJobsFault(t, func(context.Context, proc.Proc, string) (uint8, error) { return 0, nil }, nil)
	if _, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'a', Dir: "/", Script: "echo"}); err != nil {
		t.Fatal(err)
	}
	fault := &faultSystem{System: f.FS, removeErr: map[string]error{AtSpoolDir + "/a0001": errno.IO}}
	f.injectFault(fault)
	if err := f.Kernel.RunDueJobs(ctx); !errors.Is(err, errno.IO) {
		t.Fatalf("认领失败应打断检查点: %v", err)
	}
	if len(f.runs) != 0 {
		t.Fatalf("认领失败不得执行作业: %v", f.runs)
	}
	if jobs, err := f.Kernel.ListJobs(ctx); err != nil || len(jobs) != 1 {
		t.Fatalf("认领失败后作业必须还在 spool 里: %v %v", jobs, err)
	}

	if err := f.Kernel.runAtJob(ctx, pendingJob{name: "a0404", dir: "/"}); err != nil {
		t.Fatalf("已被取走的作业应静默跳过: %v", err)
	}
	if len(f.runs) != 0 {
		t.Fatalf("未认领的作业不得执行: %v", f.runs)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := f.Kernel.RunDueJobs(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的检查点: %v", err)
	}
	if err := f.Kernel.Close(ctx); err != nil {
		t.Fatal(err)
	}
	f.injectFault(&faultSystem{System: f.FS})
	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatalf("关闭后的检查点应静默跳过: %v", err)
	}
	if len(f.runs) != 0 {
		t.Fatalf("关闭后不得执行作业: %v", f.runs)
	}
}

func TestSpoolCorruptionIsSkippedWithoutBlockingQueue(t *testing.T) {
	ctx := context.Background()
	f := newJobsFault(t, func(context.Context, proc.Proc, string) (uint8, error) { return 0, nil }, nil)
	corrupt := map[string]string{
		"a0001": "#! deadline: 明天\n#! nice: 0\n#! dir: /\necho bad-deadline\n",
		"a0002": "#! deadline: 0\n#! nice: x\n#! dir: /\necho bad-nice\n",
		"a0003": "#! deadline: 0\n#! nice: 0\necho missing-dir\n",
		"a0004": "#! deadline: 0\n#! nice: 0\n#! dir: relative\necho relative-dir\n",
		"a0005": "#! deadline: 0\n#! nice: 99\n#! dir: /\necho nice-out-of-range\n",
	}
	for name, data := range corrupt {
		guestWrite(t, f.FS, AtSpoolDir+"/"+name, data)
	}

	guestWrite(t, f.FS, AtSpoolDir+"/a0006", "#! deadline: 0\n#! nice: 0\n#! dir: /")

	guestWrite(t, f.FS, AtSpoolDir+"/a0007", "#! deadline: 999999999\n#! nice: 0\n#! dir: /\necho later\n")
	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.runs) != 1 || f.runs[0] != "" {
		t.Fatalf("只有空脚本作业应执行: %v", f.runs)
	}
	jobs, err := f.Kernel.ListJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a0001", "a0002", "a0003", "a0004", "a0005", "a0007"}
	if strings.Join(jobs, ",") != strings.Join(want, ",") {
		t.Fatalf("损坏与未到期作业必须留在 spool: %v", jobs)
	}

	for name, wantText := range map[string]string{
		"a0001": "到期时刻无效",
		"a0002": "优先级无效",
		"a0003": "缺少有效目录",
		"a0004": "缺少有效目录",
		"a0005": "优先级无效",
	} {
		_, err := readAtJob(ctx, f.FS, AtSpoolDir, name)
		if err == nil || !strings.Contains(err.Error(), wantText) {
			t.Fatalf("%s 应报 %q: %v", name, wantText, err)
		}
	}

	if _, err := readAtJob(ctx, f.FS, AtSpoolDir, "a9999"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("缺失的作业文件应报 ENOENT: %v", err)
	}

	fault := &faultSystem{System: f.FS, readErr: map[string]error{AtSpoolDir + "/a0001": errno.IO}, closeErr: map[string]error{AtSpoolDir + "/a0002": errno.IO}}
	if _, err := readAtJob(ctx, fault, AtSpoolDir, "a0001"); !errors.Is(err, errno.IO) {
		t.Fatalf("读失败应报 EIO: %v", err)
	}
	if _, err := readAtJob(ctx, fault, AtSpoolDir, "a0002"); !errors.Is(err, errno.IO) {
		t.Fatalf("关闭失败应报 EIO: %v", err)
	}
}

func TestQueueJobValidationAndEnvRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := newJobsFault(t, func(_ context.Context, p proc.Proc, _ string) (uint8, error) {
		if p.Env["JOB_MODE"] != "batch" || p.Env["ALPHA"] != "a b" {
			t.Errorf("作业环境不符: %v", p.Env)
		}
		if p.Nice != 3 {
			t.Errorf("作业应以提交时刻的 nice 运行: %d", p.Nice)
		}
		return 0, nil
	}, nil)
	if _, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'c', Dir: "/", Script: "x"}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("队列只能是 a/b: %v", err)
	}
	if _, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'a', Dir: "relative", Script: "x"}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("目录必须是客体绝对路径: %v", err)
	}
	if _, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'a', Dir: "/", Nice: proc.NiceHigh + 1, Script: "x"}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("越界 nice 应拒绝: %v", err)
	}
	if _, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'a', Dir: "/", Nice: proc.NiceLow - 1, Script: "x"}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("越界 nice 应拒绝: %v", err)
	}
	id, err := f.Kernel.QueueJob(ctx, proc.JobSpec{
		Queue: 'a', Dir: "/", Nice: 3, Mail: true,
		Env:    map[string]string{"JOB_MODE": "batch", "ALPHA": "a\nb"},
		Script: "echo env",
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := f.Kernel.ReadJob(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, "#! env-ALPHA: a b\n") || !strings.Contains(data, "#! env-JOB_MODE: batch\n") {
		t.Fatalf("环境头行必须是单行且换行折成空格: %q", data)
	}
	if strings.Index(data, "env-ALPHA") > strings.Index(data, "env-JOB_MODE") {
		t.Fatalf("环境头行必须按键升序: %q", data)
	}

	if ok, err := f.Kernel.RemoveJob(ctx, "a00/1"); !errors.Is(err, errno.Invalid) || ok {
		t.Fatalf("路径片段作业号应拒绝: %v %v", ok, err)
	}
	if _, err := f.Kernel.ReadJob(ctx, "b"); !errors.Is(err, errno.Invalid) {
		t.Fatalf("过短的作业号应拒绝: %v", err)
	}
	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.runs) != 1 {
		t.Fatalf("作业应执行一次: %v", f.runs)
	}
}

func TestPrintRequestFailuresAreReportedAndConsumed(t *testing.T) {
	ctx := context.Background()
	f := newJobsFault(t, nil, nil)
	request, err := f.Kernel.QueuePrint(ctx, proc.PrintSpec{
		Destination: "lp0", Copies: 2, Copy: true, Title: "月报", Notify: true, Write: true, Body: "hello\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	data, ok := guestRead(t, f.FS, PrintRequestDir+"/"+request)
	if !ok {
		t.Fatalf("请求文件 %s 应存在", request)
	}
	for _, header := range []string{"#! copies: 2", "#! source: copy", "#! title: 月报", "#! mail: yes", "#! notify: yes"} {
		if !strings.Contains(data, header+"\n") {
			t.Fatalf("请求头缺少 %q: %q", header, data)
		}
	}

	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if requests, err := f.Kernel.ListPrintRequests(ctx); err != nil || len(requests) != 0 {
		t.Fatalf("请求处理后应清空: %v %v", requests, err)
	}
	mail, ok := guestRead(t, f.FS, MailDir+"/sandbox")
	if !ok || !strings.Contains(mail, "打印请求 "+request+" 已完成") {
		t.Fatalf("终端不可得时完成通知应退化为邮件: %q ok=%v", mail, ok)
	}

	guestWrite(t, f.FS, PrintRequestDir+"/lp0-0500", "#! copies: 0\n正文\n")
	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	mail, _ = guestRead(t, f.FS, MailDir+"/sandbox")
	if !strings.Contains(mail, "打印请求 lp0-0500 无法解析") {
		t.Fatalf("损坏请求应投递诊断: %q", mail)
	}
	if _, ok := guestRead(t, f.FS, PrintRequestDir+"/lp0-0500"); ok {
		t.Fatal("损坏请求也必须被认领，否则会永久卡住队列")
	}

	guestWrite(t, f.FS, PrintRequestDir+"/lp0-0501", "#! copies: 1\n#! source: reference\n/work/missing\n")
	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	mail, _ = guestRead(t, f.FS, MailDir+"/sandbox")

	if !strings.Contains(mail, "打印请求 lp0-0501 失败") || !strings.Contains(mail, "引用的文件 /work/missing") || !strings.Contains(mail, "读不到") {
		t.Fatalf("引用缺失应投递诊断: %q", mail)
	}

	guestWrite(t, f.FS, "/work/doc", "报告正文")
	guestWrite(t, f.FS, PrintRequestDir+"/lp0-0502", "#! copies: 1\n#! source: reference\n/work/doc")
	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	sink, _ := guestRead(t, f.FS, PrintSink)
	if !strings.Contains(sink, "报告正文") {
		t.Fatalf("引用型请求应打印源文件内容: %q", sink)
	}

	broken := newJobsFault(t, nil, nil)
	guestWrite(t, broken.FS, PrintSink+"/blocker", "x")
	guestWrite(t, broken.FS, PrintRequestDir+"/lp0-0503", "#! copies: 1\n#! source: copy\n内容\n")
	if err := broken.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	mail, _ = guestRead(t, broken.FS, MailDir+"/sandbox")
	if !strings.Contains(mail, "打印请求 lp0-0503 写入目的地失败") {
		t.Fatalf("落点写入失败应投递诊断: %q", mail)
	}
	if _, ok := guestRead(t, broken.FS, PrintRequestDir+"/lp0-0503"); ok {
		t.Fatal("写入失败的请求也应离开 spool")
	}
}

func TestPrintRequestCopySeparatorFailure(t *testing.T) {
	ctx := context.Background()
	f := newJobsFault(t, nil, nil)
	guestWrite(t, f.FS, PrintRequestDir+"/lp0-0600", "#! copies: 2\n#! source: copy\nbody\n")

	f.injectFault(&faultSystem{System: f.FS, openAfter: map[string]int{PrintSink: 1}})
	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	mail, ok := guestRead(t, f.FS, MailDir+"/sandbox")
	if !ok || !strings.Contains(mail, "打印请求 lp0-0600 写入目的地失败") {
		t.Fatalf("换页写入失败应投递诊断: %q ok=%v", mail, ok)
	}
	if _, ok := guestRead(t, f.FS, PrintRequestDir+"/lp0-0600"); ok {
		t.Fatal("失败的请求应已认领")
	}
}

func TestPrintOneMissingRequestIsSilent(t *testing.T) {
	ctx := context.Background()
	f := newJobsFault(t, nil, nil)
	if err := f.Kernel.printOne(ctx, "lp0-0700"); err != nil {
		t.Fatalf("缺失的请求应静默跳过: %v", err)
	}
}

func TestPrintDrainReportsBackendFailures(t *testing.T) {
	ctx := context.Background()
	f := newJobsFault(t, nil, nil)
	f.injectFault(&faultSystem{System: f.FS, readDirErr: map[string]error{PrintRequestDir: errno.IO}})
	if err := f.Kernel.RunDueJobs(ctx); !errors.Is(err, errno.IO) {
		t.Fatalf("请求目录读不到应报 EIO: %v", err)
	}
	f.injectFault(&faultSystem{System: f.FS, removeErr: map[string]error{PrintRequestDir + "/lp0-0800": errno.IO}})
	guestWrite(t, f.FS, PrintRequestDir+"/lp0-0800", "#! copies: 1\n#! source: copy\n内容\n")
	if err := f.Kernel.RunDueJobs(ctx); !errors.Is(err, errno.IO) {
		t.Fatalf("请求删不掉应报 EIO: %v", err)
	}
	if _, ok := guestRead(t, f.FS, PrintRequestDir+"/lp0-0800"); !ok {
		t.Fatal("认领失败的请求必须留在 spool 里")
	}
}

type fakeTerminal struct {
	out      bytes.Buffer
	writeErr error
}

func (f *fakeTerminal) Dimensions() (uint16, uint16) { return 24, 80 }
func (f *fakeTerminal) Read([]byte) (int, error)     { return 0, errno.WouldBlock }

func (f *fakeTerminal) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.out.Write(p)
}

func (f *fakeTerminal) Close() error { return nil }

func TestPrintTerminalNotificationPaths(t *testing.T) {
	ctx := context.Background()
	f := newJobsFault(t, nil, nil)

	f.Kernel.fds.Attach(uint64(f.Kernel.Init()), fd.Streams{Output: &bytes.Buffer{}})
	terminal := &fakeTerminal{}
	if err := f.Kernel.InstallFD(f.Kernel.Init(), 1, terminal); err != nil {
		t.Fatal(err)
	}
	guestWrite(t, f.FS, PrintRequestDir+"/lp0-0900", "#! copies: 1\n#! source: copy\n#! mail: yes\n#! notify: yes\n内容\n")
	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(terminal.out.String(), "打印请求 lp0-0900 已完成") {
		t.Fatalf("完成通知应写进终端: %q", terminal.out.String())
	}
	if _, ok := guestRead(t, f.FS, MailDir+"/sandbox"); ok {
		t.Fatal("终端通知成功时不应再投递邮件（-w 优先于 -m）")
	}

	plain := &bytes.Buffer{}
	if err := f.Kernel.InstallFD(f.Kernel.Init(), 1, &bytesReadWriter{buffer: plain}); err != nil {
		t.Fatal(err)
	}
	guestWrite(t, f.FS, PrintRequestDir+"/lp0-0901", "#! copies: 1\n#! source: copy\n#! notify: yes\n内容\n")
	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	mail, ok := guestRead(t, f.FS, MailDir+"/sandbox")
	if !ok || !strings.Contains(mail, "打印请求 lp0-0901 已完成") {
		t.Fatalf("非终端应退化为邮件: %q ok=%v", mail, ok)
	}

	if err := f.Kernel.InstallFD(f.Kernel.Init(), 1, &fakeTerminal{writeErr: errno.IO}); err != nil {
		t.Fatal(err)
	}
	guestWrite(t, f.FS, PrintRequestDir+"/lp0-0902", "#! copies: 1\n#! source: copy\n#! notify: yes\n内容\n")
	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	mail, _ = guestRead(t, f.FS, MailDir+"/sandbox")
	if !strings.Contains(mail, "打印请求 lp0-0902 已完成") {
		t.Fatalf("终端写入失败应退化为邮件: %q", mail)
	}

	guestWrite(t, f.FS, PrintRequestDir+"/lp0-0903", "#! copies: 1\n#! source: copy\n#! mail: yes\n#! notify: yes\n内容\n")
	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	mail, _ = guestRead(t, f.FS, MailDir+"/sandbox")
	if !strings.Contains(mail, "打印请求 lp0-0903 已完成") {
		t.Fatalf("-w 与 -m 同时声明且终端不可达时通知不能丢失: %q", mail)
	}
}

type bytesReadWriter struct{ buffer *bytes.Buffer }

func (b *bytesReadWriter) Read([]byte) (int, error) { return 0, errno.WouldBlock }
func (b *bytesReadWriter) Write(p []byte) (int, error) {
	return b.buffer.Write(p)
}
func (b *bytesReadWriter) Close() error { return nil }

func TestJobMailFailureIsLoggedNotFatal(t *testing.T) {
	ctx := context.Background()
	f := newJobsFault(t, func(ctx context.Context, p proc.Proc, _ string) (uint8, error) {
		return 0, writeStdout(ctx, p, "job output\n")
	}, nil)
	f.injectFault(&faultSystem{System: f.FS, writeErr: map[string]error{MailDir + "/sandbox": errno.IO}})
	if _, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'a', Dir: "/", Script: "report"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatalf("投递失败不应让检查点失败: %v", err)
	}
	if len(f.runs) != 1 {
		t.Fatalf("作业应已真实执行: %v", f.runs)
	}
	if jobs, _ := f.Kernel.ListJobs(ctx); len(jobs) != 0 {
		t.Fatalf("作业应已离开 spool: %v", jobs)
	}
	logged, ok := guestRead(t, f.FS, "/var/log/messages")
	if !ok || !strings.Contains(logged, "邮件投递失败") {
		t.Fatalf("投递失败应进系统日志: %q ok=%v", logged, ok)
	}
}

func TestJobStartFailureReportsExit127(t *testing.T) {
	ctx := context.Background()
	f := newJobsFault(t, nil, nil)
	if _, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'a', Dir: "/", Script: "echo"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	mail, ok := guestRead(t, f.FS, MailDir+"/sandbox")
	if !ok || !strings.Contains(mail, "作业无法启动") || !strings.Contains(mail, "以退出码 127 结束") {
		t.Fatalf("启动失败应如实报告: %q ok=%v", mail, ok)
	}
	if jobs, _ := f.Kernel.ListJobs(ctx); len(jobs) != 0 {
		t.Fatalf("启动失败的作业也应离开 spool（认领后执行）: %v", jobs)
	}
}

func TestJobOutputTruncationIsMarked(t *testing.T) {
	ctx := context.Background()
	f := newJobsFault(t, func(ctx context.Context, p proc.Proc, _ string) (uint8, error) {
		return 0, writeStdout(ctx, p, strings.Repeat("x", jobOutputLimit+4096))
	}, nil)
	if _, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'a', Dir: "/", Mail: true, Script: "noisy"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	mail, ok := guestRead(t, f.FS, MailDir+"/sandbox")
	if !ok || !strings.Contains(mail, "[输出超出收集上限，已截断]") {
		t.Fatalf("截断必须注明: ok=%v", ok)
	}
	if len(mail) > jobOutputLimit+4096 {
		t.Fatalf("收集缓冲必须有界: %d", len(mail))
	}
}

func TestRunDueJobsSkipsReentrantCheckpoint(t *testing.T) {
	ctx := context.Background()
	var kernel *Kernel
	var nested []error
	f := newJobsFault(t, func(ctx context.Context, _ proc.Proc, _ string) (uint8, error) {
		nested = append(nested, kernel.RunDueJobs(ctx))
		return 0, nil
	}, nil)
	kernel = f.Kernel
	if _, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'a', Dir: "/", Script: "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'a', Dir: "/", Script: "second"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}

	if len(nested) != 2 {
		t.Fatalf("每个作业各触发一次内层检查点: %v", nested)
	}
	for _, err := range nested {
		if err != nil {
			t.Fatalf("重入的检查点应被守护: %v", nested)
		}
	}
	if strings.Join(f.runs, ",") != "first,second" {
		t.Fatalf("两个作业各执行一次且不嵌套: %v", f.runs)
	}
}

type fakeClockSource struct {
	now        uint64
	sleepErr   error
	monoErr    error
	overshoot  uint64
	undershoot uint64

	monoErrOn int
	monoCalls int
	sleeps    []uint64
}

func (c *fakeClockSource) Wall(context.Context) (int64, error) {
	if c.now > 1<<63-1 {
		return 0, errno.Range
	}
	return int64(c.now), nil
}

func (c *fakeClockSource) Monotonic(context.Context) (uint64, error) {
	c.monoCalls++
	if c.monoErr != nil {
		return 0, c.monoErr
	}
	if c.monoErrOn != 0 && c.monoCalls == c.monoErrOn {
		return 0, errno.IO
	}
	return c.now, nil
}

func (c *fakeClockSource) Sleep(_ context.Context, nanoseconds uint64) error {
	if c.sleepErr != nil {
		return c.sleepErr
	}
	c.sleeps = append(c.sleeps, nanoseconds)
	c.now += nanoseconds + c.overshoot - c.undershoot
	return nil
}

func TestYieldClockSleepBoundaries(t *testing.T) {
	ctx := context.Background()

	fake := &fakeClockSource{now: 10}
	f := newJobsFault(t, func(context.Context, proc.Proc, string) (uint8, error) { return 0, nil }, func(services *Services) {
		services.Clock = fake
	})
	wrapped, ok := f.Kernel.services.Clock.(yieldClock)
	if !ok {
		t.Fatalf("能力视图应持有分段时钟: %T", f.Kernel.services.Clock)
	}

	if err := wrapped.Sleep(ctx, math.MaxUint64); !errors.Is(err, errno.Overflow) {
		t.Fatalf("回绕应报 EOVERFLOW: %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := wrapped.Sleep(cancelled, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应上报: %v", err)
	}

	fake.monoErr = errno.IO
	if err := wrapped.Sleep(ctx, 5); !errors.Is(err, errno.IO) {
		t.Fatalf("读数失败应上报: %v", err)
	}
	fake.monoErr = nil
	fake.sleepErr = errno.IO
	if err := wrapped.Sleep(ctx, 5); !errors.Is(err, errno.IO) {
		t.Fatalf("睡眠失败应上报: %v", err)
	}
	fake.sleepErr = nil

	fake.now = 0
	fake.sleeps = nil
	if err := wrapped.Sleep(ctx, 50); err != nil {
		t.Fatal(err)
	}
	if len(fake.sleeps) != 1 || fake.sleeps[0] != 50 {
		t.Fatalf("无到期作业时应整体睡眠: %v", fake.sleeps)
	}

	if _, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'a', Dir: "/", Deadline: fake.now + 5, Script: "due"}); err != nil {
		t.Fatal(err)
	}
	fake.sleeps = nil
	fake.overshoot = 1000
	if err := wrapped.Sleep(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if len(fake.sleeps) != 1 || fake.sleeps[0] != 5 {
		t.Fatalf("应睡到最早到期点: %v", fake.sleeps)
	}
	if strings.Join(f.runs, ",") != "due" {
		t.Fatalf("到期作业应在分段点执行: %v", f.runs)
	}
}

func TestYieldClockDeadlineAndSpoolFailures(t *testing.T) {
	ctx := context.Background()
	f := newJobsFault(t, func(context.Context, proc.Proc, string) (uint8, error) { return 0, nil }, nil)

	noFS := newJobsFault(t, nil, func(services *Services) { services.FS = nil })
	clockOver := yieldClock{inner: &fakeClockSource{}, k: noFS.Kernel}
	if err := clockOver.Sleep(ctx, 10); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("缺能力应上报: %v", err)
	}

	f.injectFault(&faultSystem{System: f.FS, readDirErr: map[string]error{AtSpoolDir: errno.IO}})
	if err := (yieldClock{inner: &fakeClockSource{}, k: f.Kernel}).Sleep(ctx, 10); !errors.Is(err, errno.IO) {
		t.Fatalf("spool 读不到应上报: %v", err)
	}

	f.injectFault(&faultSystem{System: f.FS})
	guestWrite(t, f.FS, AtSpoolDir+"/a0001", "#! deadline: 坏\n#! nice: 0\n#! dir: /\nx\n")
	guestWrite(t, f.FS, AtSpoolDir+"/a0002", "#! deadline: 0\n#! nice: 0\n#! dir: /\nx\n")
	guestWrite(t, f.FS, AtSpoolDir+"/a0003", "#! deadline: 30\n#! nice: 0\n#! dir: /\necho later\n")
	inner := &fakeClockSource{}
	if err := (yieldClock{inner: inner, k: f.Kernel}).Sleep(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if len(inner.sleeps) != 2 || inner.sleeps[0] != 30 {
		t.Fatalf("应在最早的合法到期点分段: %v", inner.sleeps)
	}
}

func TestSpoolHelperContracts(t *testing.T) {
	for _, value := range []string{"18446744073709551615", "-18446744073709551615"} {
		if _, err := parseNice(value); !errors.Is(err, errno.Invalid) {
			t.Fatalf("parseNice(%q) 应拒绝: %v", value, err)
		}
	}
	if _, _, err := parsePrintRequest("#! copies: 9223372036854775808\nbody"); err == nil {
		t.Fatal("超出 int 范围的打印份数应被拒绝")
	}
	for _, bad := range []string{"", "12a", "-1", strings.Repeat("9", 25)} {
		if _, err := parseUint(bad); !errors.Is(err, errno.Invalid) {
			t.Fatalf("parseUint(%q) 应拒绝: %v", bad, err)
		}
	}
	if value, err := parseUint(strings.Repeat("9", 18)); err != nil || value != 999999999999999999 {
		t.Fatalf("parseUint 合法值: %d %v", value, err)
	}
	for _, bad := range []string{"", "a", "a123", "c0001", "a00x1", "a/0001"} {
		if validJobName(bad) {
			t.Fatalf("%q 不应是合法作业名", bad)
		}
	}
	if !validJobName("a0001") || !validJobName("b12345") {
		t.Fatal("合法作业名被拒")
	}
	if keys := sortedKeys(map[string]string{"b": "2", "a": "1"}); strings.Join(keys, ",") != "a,b" {
		t.Fatalf("键序必须确定: %v", keys)
	}

	for _, days := range []int64{-800000, -719469, -1, 0, 1, 20000} {
		year, month, day := civilFromDays(days)
		want := time.Unix(days*86400, 0).UTC()
		if year != int64(want.Year()) || month != int(want.Month()) || day != want.Day() {
			t.Fatalf("civilFromDays(%d) = %d-%02d-%02d, want %s", days, year, month, day, want.Format("2006-01-02"))
		}
	}
}

func TestMailDeliveryUsesDeclaredIdentity(t *testing.T) {
	ctx := context.Background()
	f := newJobsFault(t, nil, func(services *Services) {
		services.Set.Identity = declaredIdentity{name: "ops"}
		services.Clock = nil
	})
	if err := f.Kernel.deliverMail(ctx, "主题", "正文"); err != nil {
		t.Fatal(err)
	}
	mail, ok := guestRead(t, f.FS, MailDir+"/ops")
	if !ok {
		t.Fatal("收件人应是声明身份的名字")
	}
	if !strings.Contains(mail, "To: ops") || !strings.Contains(mail, "Thu Jan  1") {
		t.Fatalf("邮件头不符（身份或无时钟回退）: %q", mail)
	}
	if err := f.Kernel.deliverMail(ctx, "主题二", "第一行\nFrom 伪造的发件人行"); err != nil {
		t.Fatal(err)
	}
	mail, _ = guestRead(t, f.FS, MailDir+"/ops")
	if !strings.Contains(mail, "\n>From 伪造的发件人行") {
		t.Fatalf("体首 From 必须转义: %q", mail)
	}
}

func TestPrintMailOnlyAndSpoolReadLimit(t *testing.T) {
	ctx := context.Background()
	f := newJobsFault(t, nil, nil)
	guestWrite(t, f.FS, PrintRequestDir+"/lp0-1000", "#! copies: 1\n#! source: copy\n#! mail: yes\n内容\n")
	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	mail, ok := guestRead(t, f.FS, MailDir+"/sandbox")
	if !ok || !strings.Contains(mail, "打印请求 lp0-1000 已完成") {
		t.Fatalf("-m 完成通知应走邮件: %q ok=%v", mail, ok)
	}

	const headerText = "#! deadline: 0\n#! nice: 0\n#! dir: /\n"
	body := strings.Repeat("a", jobOutputLimit+64)
	g := newJobsFault(t, func(_ context.Context, _ proc.Proc, script string) (uint8, error) {
		if len(script) != jobOutputLimit-len(headerText) {
			t.Errorf("脚本应在读取上限处截断: %d", len(script))
		}
		return 0, nil
	}, nil)
	guestWrite(t, g.FS, AtSpoolDir+"/a0001", headerText+body)
	if err := g.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if len(g.runs) != 1 {
		t.Fatalf("截断的作业仍应执行: %v", g.runs)
	}
}

func TestSchedulerEntriesReportClockFailures(t *testing.T) {
	ctx := context.Background()
	f := newJobsFault(t, func(context.Context, proc.Proc, string) (uint8, error) { return 0, nil }, func(services *Services) {
		services.Clock = &fakeClockSource{monoErr: errno.IO}
	})
	if err := f.Kernel.RunDueJobs(ctx); !errors.Is(err, errno.IO) {
		t.Fatalf("检查点的读数失败应上报: %v", err)
	}
	if err := f.Kernel.deliverMail(ctx, "主题", "正文"); !errors.Is(err, errno.IO) {
		t.Fatalf("投递的读数失败应上报: %v", err)
	}
	if err := f.Kernel.logNotice(ctx, "jobs", "诊断"); !errors.Is(err, errno.IO) {
		t.Fatalf("诊断的读数失败应上报: %v", err)
	}
}

func TestSpoolSequenceIgnoresStrayNames(t *testing.T) {
	ctx := context.Background()
	f := newJobsFault(t, nil, nil)
	guestWrite(t, f.FS, AtSpoolDir+"/notes.txt", "x")
	guestWrite(t, f.FS, AtSpoolDir+"/aZZZZ", "x")
	id, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'a', Dir: "/", Script: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "a0001" {
		t.Fatalf("无关文件不得推高序号: %q", id)
	}
	if jobs, _ := f.Kernel.ListJobs(ctx); len(jobs) != 3 {
		t.Fatalf("无关文件不参与执行但仍是 spool 条目: %v", jobs)
	}

	f.injectFault(&faultSystem{System: f.FS, readDirErr: map[string]error{PrintRequestDir: errno.IO}})
	if _, err := f.Kernel.QueuePrint(ctx, proc.PrintSpec{Destination: "lp0", Copies: 1, Copy: true}); !errors.Is(err, errno.IO) {
		t.Fatalf("序号探测失败应报 EIO: %v", err)
	}
}

func TestPrintEntriesReportCapabilityAndReadFaults(t *testing.T) {
	ctx := context.Background()
	noFS := newJobsFault(t, nil, func(services *Services) { services.FS = nil })
	if err := noFS.Kernel.drainPrintQueue(ctx); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("drainPrintQueue: %v", err)
	}
	if err := noFS.Kernel.printOne(ctx, "lp0-0001"); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("printOne: %v", err)
	}
	if _, err := noFS.Kernel.printSource(ctx, printRequest{}, "/work/doc"); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("printSource: %v", err)
	}
	f := newJobsFault(t, nil, nil)

	f.injectFault(&faultSystem{System: f.FS, openErr: map[string]error{PrintRequestDir + "/lp0-0002": errno.IO}})
	if err := f.Kernel.printOne(ctx, "lp0-0002"); !errors.Is(err, errno.IO) {
		t.Fatalf("打开失败应上报: %v", err)
	}

	f.injectFault(&faultSystem{System: f.FS, readErr: map[string]error{PrintRequestDir + "/lp0-0003": errno.IO}})
	guestWrite(t, f.FS, PrintRequestDir+"/lp0-0003", "#! copies: 1\nx")
	if err := f.Kernel.printOne(ctx, "lp0-0003"); !errors.Is(err, errno.IO) {
		t.Fatalf("读请求失败应上报: %v", err)
	}

	f.injectFault(&faultSystem{System: f.FS, closeErr: map[string]error{PrintRequestDir + "/lp0-0004": errno.IO}})
	guestWrite(t, f.FS, PrintRequestDir+"/lp0-0004", "#! copies: 1\nx")
	if err := f.Kernel.printOne(ctx, "lp0-0004"); !errors.Is(err, errno.IO) {
		t.Fatalf("关闭请求失败应上报: %v", err)
	}

	guestWrite(t, f.FS, "/work/doc2", "正文")
	f.injectFault(&faultSystem{System: f.FS, readErr: map[string]error{"/work/doc2": errno.IO}})
	guestWrite(t, f.FS, PrintRequestDir+"/lp0-0005", "#! copies: 1\n#! source: reference\n/work/doc2")
	if err := f.Kernel.printOne(ctx, "lp0-0005"); err != nil {
		t.Fatal(err)
	}
	mail, _ := guestRead(t, f.FS, MailDir+"/sandbox")
	if !strings.Contains(mail, "打印请求 lp0-0005 失败") {
		t.Fatalf("引用读取失败应投递诊断: %q", mail)
	}

	guestWrite(t, f.FS, PrintRequestDir+"/lp0-0006", "#! copies: 1\nx")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := f.Kernel.drainPrintQueue(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("排空循环应尊重取消: %v", err)
	}

	f.injectFault(&faultSystem{System: f.FS, statErr: map[string]error{MailDir: errno.IO}})
	if err := f.Kernel.deliverMail(ctx, "主题", "正文"); !errors.Is(err, errno.IO) {
		t.Fatalf("投递失败应上报: %v", err)
	}
}

func TestCheckpointStopsBetweenJobsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	f := newJobsFault(t, func(context.Context, proc.Proc, string) (uint8, error) {
		once.Do(cancel)
		return 0, nil
	}, nil)
	for _, script := range []string{"first", "second"} {
		if _, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'a', Dir: "/", Script: script}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Kernel.RunDueJobs(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("作业之间必须尊重取消: %v", err)
	}
	if strings.Join(f.runs, ",") != "first" {
		t.Fatalf("取消后不得继续跑下一个作业: %v", f.runs)
	}
	if jobs, _ := f.Kernel.ListJobs(context.Background()); len(jobs) != 1 {
		t.Fatalf("剩下的作业应留在 spool: %v", jobs)
	}
}

func TestYieldClockSegmentedFailures(t *testing.T) {
	ctx := context.Background()

	sleepFake := &fakeClockSource{sleepErr: errno.IO}
	sleepFixture := newJobsFault(t, nil, func(services *Services) { services.Clock = sleepFake })
	guestWrite(t, sleepFixture.FS, AtSpoolDir+"/a0001", "#! deadline: 30\n#! nice: 0\n#! dir: /\nx\n")
	if err := sleepFixture.Kernel.services.Set.Clock.(yieldClock).Sleep(ctx, 100); !errors.Is(err, errno.IO) {
		t.Fatalf("分段睡眠失败应上报: %v", err)
	}

	faultFake := &fakeClockSource{}
	faultFixture := newJobsFault(t, nil, func(services *Services) { services.Clock = faultFake })
	faultFixture.injectFault(&faultSystem{System: faultFixture.FS, removeErr: map[string]error{AtSpoolDir + "/a0001": errno.IO}})
	guestWrite(t, faultFixture.FS, AtSpoolDir+"/a0001", "#! deadline: 30\n#! nice: 0\n#! dir: /\nx\n")
	if err := faultFixture.Kernel.services.Set.Clock.(yieldClock).Sleep(ctx, 100); !errors.Is(err, errno.IO) {
		t.Fatalf("分段点的检查点失败应上报: %v", err)
	}

	monoFake := &fakeClockSource{undershoot: 1, monoErrOn: 3}
	monoFixture := newJobsFault(t, nil, func(services *Services) { services.Clock = monoFake })
	guestWrite(t, monoFixture.FS, AtSpoolDir+"/a0001", "#! deadline: 30\n#! nice: 0\n#! dir: /\nx\n")
	if err := monoFixture.Kernel.services.Set.Clock.(yieldClock).Sleep(ctx, 100); !errors.Is(err, errno.IO) {
		t.Fatalf("分段后的读数失败应上报: %v", err)
	}
}
