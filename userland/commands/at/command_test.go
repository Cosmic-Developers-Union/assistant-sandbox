package at_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	atcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/at"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

func TestQueueAndListAndShow(t *testing.T) {
	f := newFixture(t)
	r := f.run(atcmd.Run, []string{"now", "+1", "minute"}, "echo hi\n")
	if r.Code != 0 || !strings.Contains(r.Stdout, "job a0001 at ") {
		t.Fatalf("排队应成功并打印作业行: %+v", r)
	}
	r = f.run(atcmd.Run, []string{"-l"}, "")
	if r.Code != 0 || !slices.Equal(strings.Fields(r.Stdout), []string{"a0001"}) {
		t.Fatalf("-l 应列出作业: %+v", r)
	}
	r = f.run(atcmd.Run, []string{"-c", "a0001"}, "")
	if r.Code != 0 || !strings.Contains(r.Stdout, "echo hi") {
		t.Fatalf("-c 应显示脚本: %+v", r)
	}

	r = f.run(atcmd.Run, []string{"-r", "a0001"}, "")
	if r.Code != 0 {
		t.Fatalf("-r 应删除: %+v", r)
	}
	r = f.run(atcmd.Run, []string{"-r", "a0001"}, "")
	if r.Code != 1 || !strings.Contains(r.Stderr, "没有这个作业") {
		t.Fatalf("重复删除应报错: %+v", r)
	}
}

func TestTimeParsing(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		spec string
		pass bool
	}{
		{"now", true},
		{"now + 3 minutes", true},
		{"5 hours", true},
		{"15:04", true},
		{"2030-01-02 03:04:05", true},
		{"2030-01-02", true},
		{"garbage", false},

		{"teatime", false},
		{"noon", false},
		{"now + 0 minutes", false},
		{"now + 3 furlongs", false},
	}
	for _, c := range cases {
		r := f.run(atcmd.Run, []string{c.spec}, "echo\n")
		if c.pass != (r.Code == 0) {
			t.Fatalf("%s: code=%d stderr=%q", c.spec, r.Code, r.Stderr)
		}
		if !c.pass {
			continue
		}
		data, err := f.readSpool("a0001")
		if err != nil {
			t.Fatalf("%s: 读 spool: %v", c.spec, err)
		}
		if !strings.Contains(data, "#! deadline: ") {
			t.Fatalf("%s: 作业文件应有 deadline 头: %q", c.spec, data)
		}
		_ = f.removeSpool("a0001")
	}
}

func TestRunAtDeadlineDuringSleep(t *testing.T) {
	f := newFixture(t)

	r := f.run(atcmd.Run, []string{"now", "+2", "minutes"}, "echo through-sleep\n")
	if r.Code != 0 {
		t.Fatalf("排队失败: %+v", r)
	}

	wrapped, ok := f.wrappedClock()
	if !ok {
		t.Fatal("内核能力视图应持有包装时钟")
	}
	if err := wrapped.Sleep(context.Background(), 5*60_000_000_000); err != nil {
		t.Fatal(err)
	}
	mail, ok := f.readMail()
	if !ok || !strings.Contains(mail, "through-sleep") {
		t.Fatalf("作业输出应投递邮件: %q ok=%v", mail, ok)
	}

	jobs, err := f.kernel.kernel.ListJobs(context.Background())
	if err != nil || len(jobs) != 0 {
		t.Fatalf("作业执行后应离开 spool: %v %v", jobs, err)
	}
}

func TestJobInheritsNice(t *testing.T) {
	f := newFixture(t)
	r := f.run(atcmd.Run, []string{"now"}, "echo x\n")
	if r.Code != 0 {
		t.Fatalf("排队失败: %+v", r)
	}
	data, err := f.readSpool("a0001")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, "#! nice: 0\n") {
		t.Fatalf("作业应记录提交进程 nice: %q", data)
	}

	f.nice = 7
	r = f.run(atcmd.Run, []string{"now"}, "echo y\n")
	if r.Code != 0 {
		t.Fatalf("第二次排队失败: %+v", r)
	}
	data, err = f.readSpool("a0002")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, "#! nice: 7\n") {
		t.Fatalf("作业应继承当前 nice: %q", data)
	}
}

func TestBatchEntrySharesImplementation(t *testing.T) {
	f := newFixture(t)

	r := f.run(atcmd.RunBatch, []string{"now"}, "echo batched\n")
	if r.Code != 0 || !strings.Contains(r.Stdout, "job b0001 at ") {
		t.Fatalf("batch 入口 = %+v", r)
	}
}

func TestQueueOptionRejectsUnknown(t *testing.T) {
	f := newFixture(t)
	r := f.run(atcmd.Run, []string{"-q", "z", "now"}, "echo\n")
	if r.Code != 2 || !strings.Contains(r.Stderr, "不是 a 或 b") {
		t.Fatalf("未知队列应拒绝: %+v", r)
	}
}

func TestMissingTimeOperand(t *testing.T) {
	f := newFixture(t)
	r := f.run(atcmd.Run, nil, "echo\n")
	if r.Code != 2 || !strings.Contains(r.Stderr, "需要时间操作数") {
		t.Fatalf("缺时间操作数: %+v", r)
	}
}

func TestFileScriptAndDeleteFlag(t *testing.T) {
	f := newFixture(t)

	if err := f.writeFile("job.sh", "echo from-file\n"); err != nil {
		t.Fatal(err)
	}
	r := f.run(atcmd.Run, []string{"-f", "job.sh", "now"}, "")
	if r.Code != 0 {
		t.Fatalf("-f 提交 = %+v", r)
	}
	data, err := f.readSpool("a0001")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, "echo from-file") {
		t.Fatalf("脚本应来自文件: %q", data)
	}

	r = f.run(atcmd.Run, []string{"-d", "a0001"}, "")
	if r.Code != 0 {
		t.Fatalf("-d 删除 = %+v", r)
	}
}

func TestMissingScriptFile(t *testing.T) {
	f := newFixture(t)
	r := f.run(atcmd.Run, []string{"-f", "nosuch.sh", "now"}, "")
	if r.Code != 1 || !strings.Contains(r.Stderr, "读不到") {
		t.Fatalf("-f 缺文件 = %+v", r)
	}
}

func TestCatMissingJob(t *testing.T) {
	f := newFixture(t)
	r := f.run(atcmd.Run, []string{"-c", "a9999"}, "")
	if r.Code != 1 || !strings.Contains(r.Stderr, "没有这个作业") {
		t.Fatalf("-c 缺作业 = %+v", r)
	}
	r = f.run(atcmd.Run, []string{"-c"}, "")
	if r.Code != 2 {
		t.Fatalf("-c 缺操作数 = %+v", r)
	}
	r = f.run(atcmd.Run, []string{"-r"}, "")
	if r.Code != 2 {
		t.Fatalf("-r 缺操作数 = %+v", r)
	}
}

func TestListWithFilterAndErrors(t *testing.T) {
	f := newFixture(t)
	if _, err := f.kernel.QueueJob(context.Background(), proc.JobSpec{Queue: 'a', Dir: f.dir, Script: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.kernel.QueueJob(context.Background(), proc.JobSpec{Queue: 'a', Dir: f.dir, Script: "y"}); err != nil {
		t.Fatal(err)
	}

	r := f.run(atcmd.Run, []string{"-l", "a0002"}, "")
	if r.Code != 0 || strings.TrimSpace(r.Stdout) != "a0002" {
		t.Fatalf("-l 过滤 = %+v", r)
	}

	r = f.run(atcmd.Run, []string{"-q", "b", "now"}, "echo\n")
	if r.Code != 0 {
		t.Fatalf("-q b 排队 = %+v", r)
	}
}

func TestParseErrorRejectsUnknownFlag(t *testing.T) {
	f := newFixture(t)
	r := f.run(atcmd.Run, []string{"--bogus", "now"}, "echo\n")
	if r.Code != 2 || !strings.Contains(r.Stderr, "参数错误") {
		t.Fatalf("未知选项应拒绝: %+v", r)
	}
	if jobs, err := f.kernel.kernel.ListJobs(context.Background()); err != nil || len(jobs) != 0 {
		t.Fatalf("用法错误不得排队: %v %v", jobs, err)
	}
}

func TestSchedulerPortMissing(t *testing.T) {
	f := newFixture(t)
	for _, args := range [][]string{{"now"}, {"-l"}, {"-r", "a0001"}, {"-c", "a0001"}} {
		r := f.runOptions(atcmd.Run, args, "echo\n", commandtest.Options{Control: noScheduler{}})
		if r.Code != 1 || !strings.Contains(r.Stderr, "没有常驻调度器") {
			t.Fatalf("%v 缺调度端口 = %+v", args, r)
		}
	}
}

func TestSchedulerFailuresAreReported(t *testing.T) {
	f := newFixture(t)

	queueFail := stubScheduler{queueJob: func(context.Context, proc.JobSpec) (string, error) {
		return "", errno.Busy
	}}
	r := f.runOptions(atcmd.Run, []string{"now"}, "echo\n", commandtest.Options{Control: queueFail})
	if r.Code != 1 || !strings.Contains(r.Stderr, "EBUSY") {
		t.Fatalf("排队失败应报告: %+v", r)
	}

	listFail := stubScheduler{listJobs: func(context.Context) ([]string, error) { return nil, errno.Busy }}
	r = f.runOptions(atcmd.Run, []string{"-l"}, "", commandtest.Options{Control: listFail})
	if r.Code != 1 || !strings.Contains(r.Stderr, "EBUSY") {
		t.Fatalf("列举失败应报告: %+v", r)
	}

	removeFail := stubScheduler{removeJob: func(_ context.Context, id string) (bool, error) {
		if id == "a0001" {
			return false, errno.Permission
		}
		return false, nil
	}}
	r = f.runOptions(atcmd.Run, []string{"-r", "a0001", "a0002"}, "", commandtest.Options{Control: removeFail})
	if r.Code != 1 || !strings.Contains(r.Stderr, "EACCES") || !strings.Contains(r.Stderr, "没有这个作业") {
		t.Fatalf("删除失败应逐条报告: %+v", r)
	}

	readFail := stubScheduler{
		listJobs: func(context.Context) ([]string, error) { return []string{"a0001", "a0002"}, nil },
		readJob: func(_ context.Context, id string) (string, error) {
			if id == "a0001" {
				return "", errno.Permission
			}
			return "#! deadline: 1\nbody\n", nil
		},
	}
	r = f.runOptions(atcmd.Run, []string{"-c", "a0001", "a0002"}, "", commandtest.Options{Control: readFail})
	if r.Code != 1 || !strings.Contains(r.Stderr, "EACCES") || !strings.Contains(r.Stdout, "body") {
		t.Fatalf("读作业失败应报告: %+v", r)
	}
}

func TestScriptSourcesFailLoudly(t *testing.T) {
	f := newFixture(t)
	if err := f.writeFile("job.sh", "echo from-file\n"); err != nil {
		t.Fatal(err)
	}

	r := f.run(withClosedStdin(t, atcmd.Run), []string{"now"}, "")
	if r.Code != 1 || !strings.Contains(r.Stderr, "at: ") {
		t.Fatalf("stdin 读故障 = %+v", r)
	}

	readFault := withCapabilities(t, atcmd.Run, func(set *capability.Set) { set.FS = readFaultFS{set.FS} })
	r = f.run(readFault, []string{"-f", "job.sh", "now"}, "")
	if r.Code != 1 || !strings.Contains(r.Stderr, "at: ") {
		t.Fatalf("-f 读故障 = %+v", r)
	}

	noFS := withCapabilities(t, atcmd.Run, func(set *capability.Set) { set.FS = nil })
	r = f.run(noFS, []string{"-f", "job.sh", "now"}, "")
	if r.Code != 1 || !strings.Contains(r.Stderr, "at: ") {
		t.Fatalf("缺文件能力 = %+v", r)
	}
	if jobs, err := f.kernel.kernel.ListJobs(context.Background()); err != nil || len(jobs) != 0 {
		t.Fatalf("脚本读失败不得排队: %v %v", jobs, err)
	}
}

func TestClockCapabilityMissing(t *testing.T) {
	f := newFixture(t)
	noClock := withCapabilities(t, atcmd.Run, func(set *capability.Set) { set.Clock = nil })
	r := f.run(noClock, []string{"now"}, "echo\n")
	if r.Code != 1 || !strings.Contains(r.Stderr, "没有时钟能力") {
		t.Fatalf("缺时钟能力 = %+v", r)
	}
}

func TestRelativeUnitsAndTimeErrors(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		spec string
		want int64
	}{
		{"now + 30 minutes", 30 * 60_000_000_000},
		{"2 hours", 2 * 3600_000_000_000},
		{"1 day", 86400_000_000_000},
		{"2 weeks", 14 * 86400_000_000_000},
	}
	for _, c := range cases {
		r := f.run(atcmd.Run, []string{c.spec}, "echo\n")
		if r.Code != 0 {
			t.Fatalf("%s 排队 = %+v", c.spec, r)
		}
		data, err := f.readSpool("a0001")
		if err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("#! deadline: %d\n", c.want); !strings.Contains(data, want) {
			t.Fatalf("%s 的 deadline 应为 %s: %q", c.spec, want, data)
		}
		if err := f.removeSpool("a0001"); err != nil {
			t.Fatal(err)
		}
	}

	r := f.run(atcmd.Run, []string{"now", "tomorrow"}, "echo\n")
	if r.Code != 2 || !strings.Contains(r.Stderr, "无效") {
		t.Fatalf("now 非法后缀 = %+v", r)
	}

	r = f.run(atcmd.Run, []string{""}, "echo\n")
	if r.Code != 2 || !strings.Contains(r.Stderr, "时间为空") {
		t.Fatalf("空时间 = %+v", r)
	}
}

func TestJobSnapshotsEnvironment(t *testing.T) {
	f := newFixture(t)
	env := map[string]string{"LANG": "C", "FOO": "bar"}
	r := f.runOptions(atcmd.Run, []string{"now"}, "echo\n", commandtest.Options{Env: env})
	if r.Code != 0 {
		t.Fatalf("排队 = %+v", r)
	}
	data, err := f.readSpool("a0001")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, "#! env-FOO: bar\n") || !strings.Contains(data, "#! env-LANG: C\n") {
		t.Fatalf("作业头应含环境快照: %q", data)
	}
}
