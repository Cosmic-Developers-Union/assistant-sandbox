package vm

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

type jobsFixture struct {
	t      *testing.T
	Kernel *Kernel
	Clock  *clock.Virtual
	FS     *memfs.FS
	log    []string
}

func newJobsFixture(t *testing.T) *jobsFixture {
	t.Helper()
	clk := &clock.Virtual{}
	filesystem, err := memfs.New(clk, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	fixture := &jobsFixture{t: t, Clock: clk, FS: filesystem}
	bash := executable.Func(func(ctx context.Context, p proc.Proc) (uint8, error) {

		script := strings.Join(p.Args, " ")
		if len(p.Args) >= 2 && p.Args[0] == "-c" {
			script = p.Args[1]
		}
		fixture.log = append(fixture.log, script)

		switch {
		case slices.Contains(p.Args, "fail"):
			return 3, nil
		case slices.Contains(p.Args, "report"):
			if out, err := p.Resources.Descriptors(); err == nil {
				if stream, err := out.Get(ctx, uint64(p.ID), 1); err == nil {
					_, _ = stream.Write([]byte("job output\n"))
				}
			}
			_ = io.Discard
		}
		return 0, nil
	})
	registry, err := executable.New(map[string]executable.Program{"bash": bash})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{
		Set: capability.Set{FS: filesystem, FD: fd.New(), Clock: clk},

		Programs: registry,
	}, 16)
	if err != nil {
		t.Fatal(err)
	}
	fixture.Kernel = kernel
	return fixture
}

func (f *jobsFixture) readOptional(ctx context.Context, name string) (string, bool) {
	file, err := f.FS.Open(ctx, name, 0, 0)
	if err != nil {
		if errors.Is(err, errno.NotFound) {
			return "", false
		}
		f.t.Fatalf("打开 %s: %v", name, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			f.t.Error(err)
		}
	}()
	data, err := io.ReadAll(file)
	if err != nil {
		f.t.Fatalf("读 %s: %v", name, err)
	}
	return string(data), true
}

func (f *jobsFixture) read(ctx context.Context, name string) string {
	data, ok := f.readOptional(ctx, name)
	if !ok {
		f.t.Fatalf("%s 应存在", name)
	}
	return data
}

func TestQueueJobRunsAtCheckpoint(t *testing.T) {
	ctx := context.Background()
	f := newJobsFixture(t)
	id, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'a', Nice: 0, Dir: "/", Script: "echo hi"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "a0001" {
		t.Fatalf("首个作业号 = %q", id)
	}

	jobs, err := f.Kernel.ListJobs(ctx)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("排队后应有 1 个作业: %v", err)
	}
	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.log) != 1 || !strings.Contains(f.log[0], "echo hi") {
		t.Fatalf("作业应在检查点执行: %v", f.log)
	}
	if jobs, _ := f.Kernel.ListJobs(ctx); len(jobs) != 0 {
		t.Fatalf("执行后 spool 应清空: %v", jobs)
	}

	if _, ok := f.readOptional(ctx, "/var/mail/sandbox"); ok {
		t.Fatal("无输出且未声明 -m 不应发邮件")
	}
}

func TestJobMailDelivery(t *testing.T) {
	ctx := context.Background()
	f := newJobsFixture(t)
	if _, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'a', Dir: "/", Script: "report"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	mail, ok := f.readOptional(ctx, "/var/mail/sandbox")
	if !ok {
		t.Fatal("有输出的作业应投递邮件")
	}
	if !strings.HasPrefix(mail, "From ") || !strings.Contains(mail, "Subject: 客体作业 a0001 的输出") {
		t.Fatalf("邮件格式不对: %q", mail)
	}
	if !strings.Contains(mail, "job output") {
		t.Fatalf("邮件应包含作业输出: %q", mail)
	}
}

func TestJobRunsAtVirtualDeadlineDuringSleep(t *testing.T) {
	ctx := context.Background()
	f := newJobsFixture(t)
	now, err := f.Clock.Monotonic(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'a', Dir: "/", Deadline: now + 100, Script: "echo later"})
	if err != nil {
		t.Fatal(err)
	}
	_ = id

	sleeper := f.Kernel.services.Clock
	if err := sleeper.Sleep(ctx, 200); err != nil {
		t.Fatal(err)
	}
	if len(f.log) != 1 {
		t.Fatalf("作业应在睡眠期间执行: %v", f.log)
	}
	elapsed, err := f.Clock.Monotonic(ctx)
	if err != nil || elapsed != 200 {
		t.Fatalf("睡眠总时长应为 200: %d %v", elapsed, err)
	}
}

func TestNiceOrdersDueJobs(t *testing.T) {
	ctx := context.Background()
	f := newJobsFixture(t)
	for _, spec := range []proc.JobSpec{
		{Queue: 'a', Nice: 5, Dir: "/", Script: "nice5"},
		{Queue: 'a', Nice: -3, Dir: "/", Script: "nice-3"},
		{Queue: 'a', Nice: 0, Dir: "/", Script: "nice0-first"},
		{Queue: 'a', Nice: 0, Dir: "/", Script: "nice0-second"},
	} {
		if _, err := f.Kernel.QueueJob(ctx, spec); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"nice-3", "nice0-first", "nice0-second", "nice5"}
	if !slices.Equal(f.log, want) {
		t.Fatalf("执行顺序 = %v, want %v", f.log, want)
	}
}

func TestBatchDefersToDueAtJobs(t *testing.T) {
	ctx := context.Background()
	f := newJobsFixture(t)
	if _, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'b', Dir: "/", Script: "batch-job"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'a', Dir: "/", Script: "at-job"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.log, []string{"at-job", "batch-job"}) {
		t.Fatalf("batch 应让位于到期的 at 作业: %v", f.log)
	}

	if _, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'b', Dir: "/", Script: "batch-only"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.log, []string{"at-job", "batch-job", "batch-only"}) {
		t.Fatalf("batch 单独时照常执行: %v", f.log)
	}
}

func TestJobInheritsSubmitterNiceAndRunsToCompletion(t *testing.T) {
	ctx := context.Background()
	f := newJobsFixture(t)
	if _, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'a', Nice: 7, Dir: "/", Script: "echo x", Mail: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'a', Nice: 7, Dir: "/", Script: "fail", Mail: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := f.Kernel.ListJobs(ctx); len(jobs) != 0 {
		t.Fatalf("失败的作业也应离开 spool: %v", jobs)
	}
	mail, ok := f.readOptional(ctx, "/var/mail/sandbox")
	if !ok || !strings.Contains(mail, "以退出码 3 结束") {
		t.Fatalf("失败作业应如实投递退出码: %q", mail)
	}
}

func TestRemoveJob(t *testing.T) {
	ctx := context.Background()
	f := newJobsFixture(t)
	id, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'a', Dir: "/", Script: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := f.Kernel.RemoveJob(ctx, id); err != nil || !ok {
		t.Fatalf("删除排队的作业: %v %v", ok, err)
	}
	if ok, err := f.Kernel.RemoveJob(ctx, id); err != nil || ok {
		t.Fatalf("重复删除应报不存在: %v %v", ok, err)
	}
	if _, err := f.Kernel.RemoveJob(ctx, "../../etc/passwd"); !errors.Is(err, errno.Invalid) {
		t.Fatalf("路径片段作业号应拒绝: %v", err)
	}
	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.log) != 0 {
		t.Fatalf("删除后的作业不应执行: %v", f.log)
	}
}

func TestPrintQueueDrains(t *testing.T) {
	ctx := context.Background()
	f := newJobsFixture(t)
	request, err := f.Kernel.QueuePrint(ctx, proc.PrintSpec{Destination: "lp0", Copies: 2, Copy: true, Body: "hello\n"})
	if err != nil {
		t.Fatal(err)
	}
	if request != "lp0-0001" {
		t.Fatalf("首个请求号 = %q", request)
	}
	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	sink := f.read(ctx, PrintSink)
	if sink != "hello\n\fhello\n" {
		t.Fatalf("两份副本应以换页分隔: %q", sink)
	}
	requests, err := f.Kernel.ListPrintRequests(ctx)
	if err != nil || len(requests) != 0 {
		t.Fatalf("排空后请求应清空: %v %v", requests, err)
	}

	if _, err := f.Kernel.QueuePrint(ctx, proc.PrintSpec{Destination: "lp0", Copies: 1, Copy: false, Body: "/work/doc"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Kernel.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	mail, ok := f.readOptional(ctx, "/var/mail/sandbox")
	if !ok || !strings.Contains(mail, "引用的文件 /work/doc 读不到") {
		t.Fatalf("引用缺失应投递失败通知: %q", mail)
	}
}

func TestQueuePrintRejectsUnknownDestination(t *testing.T) {
	ctx := context.Background()
	f := newJobsFixture(t)
	if _, err := f.Kernel.QueuePrint(ctx, proc.PrintSpec{Destination: "other", Copy: true}); !errors.Is(err, errno.NotFound) {
		t.Fatalf("未知目的地应拒绝: %v", err)
	}
	if _, err := f.Kernel.QueuePrint(ctx, proc.PrintSpec{Destination: "lp0", Copies: 0}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("零份数应拒绝: %v", err)
	}
}

func TestRunDueJobsIsReentrantSafe(t *testing.T) {
	ctx := context.Background()
	f := newJobsFixture(t)

	if _, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'a', Dir: "/", Script: "echo inner"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := f.Kernel.RunDueJobs(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.log) != 1 {
		t.Fatalf("作业应只执行一次: %v", f.log)
	}
}

func TestScheduledJobsFeatureDeclared(t *testing.T) {
	f := newJobsFixture(t)
	if err := f.Kernel.CheckFeature(ScheduledJobs); err != nil {
		t.Fatalf("常驻调度应声明为已具备: %v", err)
	}
}

func TestReadJobAndTimestamps(t *testing.T) {
	ctx := context.Background()
	f := newJobsFixture(t)
	id, err := f.Kernel.QueueJob(ctx, proc.JobSpec{Queue: 'a', Nice: 2, Dir: "/", Script: "echo body"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := f.Kernel.ReadJob(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, "#! nice: 2") || !strings.Contains(data, "echo body") {
		t.Fatalf("ReadJob 应返回头行+正文: %q", data)
	}
	if _, err := f.Kernel.ReadJob(ctx, "../../etc/passwd"); !errors.Is(err, errno.Invalid) {
		t.Fatalf("路径片段作业号应拒绝: %v", err)
	}
	if _, err := f.Kernel.ReadJob(ctx, "a9999"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("缺作业应 ENOENT: %v", err)
	}

	if got := utcTimestamp(0); !strings.HasPrefix(got, "1970-01-01 00:00:00") {
		t.Fatalf("utcTimestamp(0) = %q", got)
	}
	if got := mboxTimestamp(0); !strings.HasPrefix(got, "Thu Jan  1") {
		t.Fatalf("mboxTimestamp(0) = %q", got)
	}
}

func TestLogNoticeFallsBackWhenMailFails(t *testing.T) {
	ctx := context.Background()
	f := newJobsFixture(t)

	if err := f.Kernel.logNotice(ctx, "jobs", "测试诊断"); err != nil {
		t.Fatal(err)
	}
	logged, ok := f.readOptional(ctx, "/var/log/messages")
	if !ok || !strings.Contains(logged, "jobs: 测试诊断") {
		t.Fatalf("系统日志应有诊断: %q ok=%v", logged, ok)
	}
}

func TestProcControlSchedulerAndPriorityPorts(t *testing.T) {
	ctx := context.Background()
	f := newJobsFixture(t)

	parent, err := f.Kernel.Begin(0, "bash", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	control, ok := parent.Control.(proc.Scheduler)
	if !ok {
		t.Fatalf("procControl 应实现 Scheduler")
	}
	priority, ok := parent.Control.(proc.PriorityControl)
	if !ok {
		t.Fatalf("procControl 应实现 PriorityControl")
	}

	if current, err := priority.Nice(parent.ID); err != nil || current != proc.DefaultNice {
		t.Fatalf("初始 nice = %d %v", current, err)
	}
	if err := priority.SetNice(parent.ID, proc.NiceHigh+1); !errors.Is(err, errno.Invalid) {
		t.Fatalf("越界 nice 应拒绝: %v", err)
	}
	if err := priority.SetNice(parent.ID, -5); err != nil {
		t.Fatal(err)
	}
	if current, _ := priority.Nice(parent.ID); current != -5 {
		t.Fatalf("SetNice 未生效: %d", current)
	}

	id, err := control.QueueJob(ctx, proc.JobSpec{Queue: 'b', Dir: "/", Script: "echo via-port"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.ReadJob(ctx, id); err != nil {
		t.Fatal(err)
	}
	request, err := control.QueuePrint(ctx, proc.PrintSpec{Destination: "lp0", Copies: 1, Copy: true, Body: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if requests, _ := control.ListPrintRequests(ctx); len(requests) != 1 || requests[0] != request {
		t.Fatalf("打印请求列表: %v", requests)
	}
	if jobs, _ := control.ListJobs(ctx); len(jobs) != 1 {
		t.Fatalf("作业列表: %v", jobs)
	}
	if err := control.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := f.Kernel.Clock(); err != nil {
		t.Fatalf("Clock(): %v", err)
	}

	future, err := control.QueueJob(ctx, proc.JobSpec{Queue: 'a', Dir: "/", Deadline: 1 << 40, Script: "echo later"})
	if err != nil {
		t.Fatal(err)
	}
	if removed, err := control.RemoveJob(ctx, future); err != nil || !removed {
		t.Fatalf("RemoveJob = %v %v", removed, err)
	}

	increment := -99
	if _, err := parent.Control.Run(ctx, proc.RunSpec{
		Parent: parent.ID, Name: "bash", Args: []string{"-c", "true"}, Dir: "/",
		NiceAdjust: &increment,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := f.Kernel.RunRegistered(ctx, parent); err != nil {
		t.Fatal(err)
	}
	if err := f.Kernel.Finish(0, parent.ID, ExitStatus{}); err != nil {
		t.Fatal(err)
	}
}
