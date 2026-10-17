package vi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	platformfs "github.com/Cosmic-Developers-Union/assistant-sandbox/platform/filesystem"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/tty"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

type fixture struct {
	t        *testing.T
	terminal *tty.Terminal
	dir      string
	cancel   context.CancelFunc
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	terminals, err := tty.New(tty.Flags{Canonical: true, Echo: false, ISIG: false}, tty.Size{Rows: 6, Cols: 20}, 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, terminal: terminals, dir: t.TempDir()}
}

func (f *fixture) sendKeys(keys string) {
	f.t.Helper()

	if _, err := f.terminal.Input([]byte(keys)); err != nil {
		f.t.Fatalf("送键: %v", err)
	}
}

func (f *fixture) screen() []string {
	lines, _, _ := f.terminal.ScreenView()
	return lines
}

func (f *fixture) runVI(name string) <-chan error {
	t := f.t

	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	descriptors := fd.New()

	descriptors.Attach(1, fd.Streams{Input: f.terminal, Output: f.terminal, Error: f.terminal})
	for id := uint32(0); id < 3; id++ {
		if err := descriptors.Close(context.Background(), 1, id); err != nil {
			t.Fatal(err)
		}
		if err := descriptors.Install(1, id, f.terminal); err != nil {
			t.Fatal(err)
		}
	}
	set := capability.Set{
		FS:       platformFS(f.dir),
		FD:       descriptors,
		Clock:    &clock.Virtual{},
		Entropy:  &entropy.Counter{},
		Identity: identity.SingleUser{},
	}

	process := proc.Proc{ID: 1, Dir: f.dir, Resources: set, SID: 1, PGID: 1}
	args := []string(nil)
	if name != "" {
		args = []string{name}
	}
	done := make(chan error, 1)
	go func() {

		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("vi panic: %v", r)
			}
		}()
		done <- invokeReal(t, ctx, process, f.dir, args)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(f.terminal.Drain()) > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	return done
}

func platformFS(dir string) vmfs.System {
	return platformfs.NewFilesystem(dir)
}

func TestInsertWriteQuit(t *testing.T) {
	f := newFixture(t)
	done := f.runVI("doc.txt")
	f.sendKeys("ihello vi\x1b:wq\r")
	select {
	case err := <-done:
		if err != nil {
			_, row, col := f.terminal.ScreenView()
			t.Fatalf("退出: %v；光标=(%d,%d) 屏幕=%q", err, row, col, f.screen())
		}
	case <-time.After(5 * time.Second):
		f.cancel()
		<-done
		t.Fatal("vi 未退出（已取消会话）")
	}
	data, err := os.ReadFile(filepath.Join(f.dir, "doc.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello vi\n" {
		t.Fatalf("文件内容 = %q", data)
	}
}

func TestEditExistingFileAndUndo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doc.txt")
	f := &fixture{t: t, dir: filepath.Dir(path)}
	terminals, err := tty.New(tty.Flags{Canonical: true, Echo: false, ISIG: false}, tty.Size{Rows: 6, Cols: 20}, 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.terminal = terminals
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := f.runVI(filepath.Base(path))

	f.sendKeys("ddu:q!\r")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("退出: %v", err)
		}
	case <-time.After(5 * time.Second):
		f.cancel()
		<-done
		t.Fatal("vi 未退出（已取消会话）")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "alpha\nbeta\n" {
		t.Fatalf("撤销后内容 = %q", data)
	}
}

func TestQuitRefusesDirtyBuffer(t *testing.T) {
	f := newFixture(t)
	done := f.runVI("")

	f.sendKeys("ix\x1b:q\r:q!\r")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("退出: %v", err)
		}
	case <-time.After(5 * time.Second):
		f.cancel()
		<-done
		t.Fatal("vi 未退出（已取消会话）")
	}
}

func TestSearchAndCursorMoves(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	f.dir = dir
	path := filepath.Join(dir, "doc.txt")
	if err := os.WriteFile(path, []byte("one two\nthree four\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := f.runVI("doc.txt")

	f.sendKeys("/four\r:wq\r")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("退出: %v", err)
		}
	case <-time.After(5 * time.Second):
		f.cancel()
		<-done
		t.Fatal("vi 未退出（已取消会话）")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "one two\nthree four\n" {
		t.Fatalf("搜索不应改变内容: %q", data)
	}
}

func TestPlainReaderFallback(t *testing.T) {

	reader := strings.NewReader("abc")
	adapted := plainReader{reader}
	buffer := make([]byte, 2)
	n, err := adapted.ReadContext(context.Background(), buffer)
	if err != nil || n != 2 || string(buffer[:n]) != "ab" {
		t.Fatalf("ReadContext = %d %q %v", n, buffer[:n], err)
	}
	lines := splitLines("a\nb\n")
	if len(lines) != 2 || lines[0] != "a" {
		t.Fatalf("splitLines = %v", lines)
	}
	if joinLines([]string{"a", "b"}) != "a\nb\n" {
		t.Fatalf("joinLines 往返")
	}

	if got := splitLines(""); len(got) != 1 || got[0] != "" {
		t.Fatalf("空文件 = %v", got)
	}
}

func TestInsertBackspaceDirect(t *testing.T) {

	var rendered bytes.Buffer
	e := &editor{lines: []string{"ab"}, row: 0, col: 2, rows: 4, cols: 20,
		input: &scriptedInput{"X\x08Y\x1b"}, output: &rendered}
	_, inserted := e.readInsertion()

	if inserted != "Y" {
		t.Fatalf("退格后插入 = %q", inserted)
	}
}

type scriptedInput struct{ sequence string }

func (s *scriptedInput) ReadContext(_ context.Context, p []byte) (int, error) {
	if len(s.sequence) == 0 {
		return 0, io.EOF
	}
	n := copy(p, s.sequence[:1])
	s.sequence = s.sequence[1:]
	return n, nil
}

func TestRejectsNonTerminalAndFlags(t *testing.T) {

	r := commandtest.Run(t, Run, t.TempDir(), []string{"x"}, "")
	if r.Code != 1 || !strings.Contains(r.Stderr, "不是终端") {
		t.Fatalf("非终端应失败: %+v", r)
	}
	r = commandtest.Run(t, Run, t.TempDir(), []string{"-r", "x"}, "")
	if r.Code != 1 || !strings.Contains(r.Stderr, "恢复") {
		t.Fatalf("-r 应拒绝: %+v", r)
	}
	r = commandtest.Run(t, Run, t.TempDir(), []string{"-t", "tag", "x"}, "")
	if r.Code != 1 || !strings.Contains(r.Stderr, "tag") {
		t.Fatalf("-t 应拒绝: %+v", r)
	}
	r = commandtest.Run(t, Run, t.TempDir(), []string{"-w", "40", "x"}, "")
	if r.Code != 1 || !strings.Contains(r.Stderr, "窗口") {
		t.Fatalf("-w 应拒绝: %+v", r)
	}
}

func invokeReal(t *testing.T, ctx context.Context, process proc.Proc, dir string, args []string) error {
	return runInterp(t, proc.WithContext(ctx, process), dir, args)
}

func TestCursorAndEditCommands(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.txt")
	if err := os.WriteFile(path, []byte("alpha beta gamma\ndelta epsilon\nzeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	f.dir = dir
	done := f.runVI("doc.txt")

	f.sendKeys("wwb^G1Gxo inserted\x1b\x1b0xP2ddp:wq\r")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("退出: %v", err)
		}
	case <-time.After(5 * time.Second):
		f.cancel()
		<-done
		t.Fatal("vi 未退出（已取消会话）")
	}
	data, _ := os.ReadFile(path)
	text := string(data)

	if !strings.Contains(text, " inserted") {
		t.Fatalf("o 开行插入缺失: %q", text)
	}
	if text == "alpha beta gamma\ndelta epsilon\nzeta\n" {
		t.Fatalf("编辑命令未生效: %q", text)
	}
}

func TestSearchNavigationAndLineGoTo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.txt")
	if err := os.WriteFile(path, []byte("one\ntwo three\nfour\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	f.dir = dir
	done := f.runVI("doc.txt")

	f.sendKeys("/three\rn3G?one\r:3\r:q!\r")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("退出: %v", err)
		}
	case <-time.After(5 * time.Second):
		f.cancel()
		<-done
		t.Fatal("vi 未退出（已取消会话）")
	}
}

func TestWordBackwardAcrossLines(t *testing.T) {

	dir := t.TempDir()
	path := filepath.Join(dir, "doc.txt")
	if err := os.WriteFile(path, []byte("one two\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	f.dir = dir
	done := f.runVI("doc.txt")
	f.sendKeys("jdb:wq\r")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("退出: %v", err)
		}
	case <-time.After(5 * time.Second):
		f.cancel()
		<-done
		t.Fatal("vi 未退出（已取消会话）")
	}
	data, _ := os.ReadFile(path)

	if string(data) != "one two\nthree\n" {
		t.Fatalf("跨行 b 后内容 = %q", data)
	}
}

func TestNewFileBannerAndWriteName(t *testing.T) {
	f := newFixture(t)
	done := f.runVI("")
	f.sendKeys("iwelcome\x1b:w newname.txt\r:q!\r")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("退出: %v", err)
		}
	case <-time.After(5 * time.Second):
		f.cancel()
		<-done
		t.Fatal("vi 未退出（已取消会话）")
	}
	data, err := os.ReadFile(filepath.Join(f.dir, "newname.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "welcome\n" {
		t.Fatalf(":w 新文件 = %q", data)
	}
}

func TestColonErrorsAndUndoEdge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.txt")
	if err := os.WriteFile(path, []byte("line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	f.dir = dir
	done := f.runVI("doc.txt")

	f.sendKeys(":bogus\ru:w othername.txt\r99G:q!\r")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("退出: %v", err)
		}
	case <-time.After(5 * time.Second):
		f.cancel()
		<-done
		t.Fatal("vi 未退出（已取消会话）")
	}

	if data, err := os.ReadFile(filepath.Join(dir, "othername.txt")); err != nil || string(data) != "line\n" {
		t.Fatalf(":w 指定文件 = %q %v", data, err)
	}
}

func TestInsertLineSplitDirect(t *testing.T) {

	var rendered bytes.Buffer
	e := &editor{lines: []string{"abcd"}, row: 0, col: 2, rows: 4, cols: 20,
		input: &scriptedInput{"XY\rZ\x1b"}, output: &rendered}
	e.insertMode(false)

	if len(e.lines) != 2 || e.lines[0] != "abXY" || e.lines[1] != "Zcd" {
		t.Fatalf("拆行 = %q", e.lines)
	}
	if e.row != 1 || e.col != 1 {
		t.Fatalf("光标 = (%d,%d), want (1,1)", e.row, e.col)
	}
}

func TestInsertModeLineSplitAndBackspace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.txt")
	if err := os.WriteFile(path, []byte("ab\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	f.dir = dir
	done := f.runVI("doc.txt")

	f.sendKeys("Icd\x1b:wq\r")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("退出: %v", err)
		}
	case <-time.After(5 * time.Second):
		f.cancel()
		<-done
		t.Fatal("vi 未退出（已取消会话）")
	}
	data, _ := os.ReadFile(path)

	if string(data) != "cdab\n" {
		t.Fatalf("插入 = %q", data)
	}
}

type scriptStep struct {
	keys string
	idle bool
	fail error
}

type scriptTerminal struct {
	mu     sync.Mutex
	steps  []scriptStep
	state  fd.TerminalState
	states []fd.TerminalState
	rows   uint16
	cols   uint16
	output bytes.Buffer
	closed bool
}

func (s *scriptTerminal) Dimensions() (uint16, uint16) { return s.rows, s.cols }

func (s *scriptTerminal) State() fd.TerminalState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *scriptTerminal) SetState(state fd.TerminalState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = state
	s.states = append(s.states, state)
	return nil
}

func (s *scriptTerminal) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	for len(s.steps) > 0 {
		step := s.steps[0]
		s.steps = s.steps[1:]
		switch {
		case step.fail != nil:
			return 0, step.fail
		case step.idle:
			return 0, nil
		case step.keys != "":
			n := copy(p, step.keys)
			if n < len(step.keys) {

				s.steps = append([]scriptStep{{keys: step.keys[n:]}}, s.steps...)
			}
			return n, nil
		}
	}
	return 0, io.EOF
}

func (s *scriptTerminal) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, errno.BadHandle
	}
	return s.output.Write(p)
}

func (s *scriptTerminal) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

type editorRun struct {
	code   int
	stderr string
	render string
	states []fd.TerminalState
}

func runEditor(t *testing.T, dir string, args []string, steps []scriptStep, tune func(*capability.Set)) editorRun {
	t.Helper()
	term := &scriptTerminal{rows: 24, cols: 80, state: fd.TerminalState{Canonical: true, Rows: 24, Columns: 80}, steps: steps}
	descriptors := fd.New()
	var stdout, stderr bytes.Buffer
	descriptors.Attach(1, fd.Streams{Input: term, Output: &stdout, Error: &stderr})
	if err := descriptors.Close(context.Background(), 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := descriptors.Install(1, 0, term); err != nil {
		t.Fatal(err)
	}
	set := capability.Set{
		FS:       platformFS(dir),
		FD:       descriptors,
		Clock:    &clock.Virtual{},
		Entropy:  &entropy.Counter{},
		Identity: identity.SingleUser{},
	}
	if tune != nil {
		tune(&set)
	}
	process := proc.Proc{ID: 1, Dir: dir, Resources: set, SID: 1, PGID: 1}
	err := invokeProcess(t, dir, args, process)
	term.mu.Lock()
	states := append([]fd.TerminalState(nil), term.states...)
	term.mu.Unlock()
	run := editorRun{stderr: stderr.String(), render: stdout.String(), states: states}
	if err != nil {
		var status interp.ExitStatus
		if !errors.As(err, &status) {
			t.Fatalf("解释器返回非退出码错误: %v", err)
		}
		run.code = int(status)
	}
	return run
}

func invokeProcess(t *testing.T, dir string, args []string, process proc.Proc) error {
	t.Helper()
	return runInterp(t, proc.WithContext(context.Background(), process), dir, args)
}

func runInterp(t *testing.T, ctx context.Context, dir string, args []string) error {
	t.Helper()
	runner, err := interp.New(
		interp.Dir(dir),
		interp.Env(expand.ListEnviron("TMPDIR="+dir)),
		interp.StdIO(strings.NewReader(""), io.Discard, io.Discard),
		interp.ExecHandlers(func(_ interp.ExecHandlerFunc) interp.ExecHandlerFunc {
			return func(ctx context.Context, _ []string) error {
				return commandio.Execute(ctx, "vi", Run, args)
			}
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := syntax.NewParser().Parse(strings.NewReader("under_test"), "test")
	if err != nil {
		t.Fatal(err)
	}
	return runner.Run(ctx, tree)
}

func newTestEditor(lines []string, steps []scriptStep) *editor {
	term := &scriptTerminal{rows: 6, cols: 20, steps: steps}
	return &editor{lines: lines, rows: 6, cols: 20, input: plainReader{term}, output: io.Discard}
}

func prepare(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func content(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("读 %s: %v", name, err)
	}
	return string(data)
}

func TestTerminalModesAreRawDuringSessionAndRestored(t *testing.T) {
	dir := t.TempDir()
	prepare(t, dir, "doc.txt", "abc\n")
	run := runEditor(t, dir, []string{"doc.txt"}, []scriptStep{{keys: "iX\x1b:wq\r"}}, nil)
	if run.code != 0 {
		t.Fatalf("退出码 = %d，stderr=%q", run.code, run.stderr)
	}
	if len(run.states) != 2 {
		t.Fatalf("终端状态切换 %d 次，want 2（进入 + 还原）: %+v", len(run.states), run.states)
	}
	if raw := run.states[0]; raw.Canonical || raw.Echo {
		t.Fatalf("进入编辑器必须非规范无回显: %+v", raw)
	}
	if restored := run.states[1]; !restored.Canonical || restored.Echo {
		t.Fatalf("退出必须还原进入前状态: %+v", restored)
	}
}

func TestCursorMotionKeySurface(t *testing.T) {
	const base = "abcde\nfghij\nklmno\n"
	cases := []struct {
		name string
		body string
		keys string
		want string
	}{

		{"方向键上下左右", base, "\x1b[B\x1b[C\x1b[Cx:wq\r", "abcde\nfgij\nklmno\n"},

		{"hljk 移动与列夹取", base, "jllkhx:wq\r", "acde\nfghij\nklmno\n"},

		{"$ 行尾与行尾 x 合并", base, "$x0x:wq\r", "bcdefghij\nklmno\n"},

		{"^ 跳过行首空白", "   x\n", "^x:wq\r", "   \n"},

		{"计数前缀删除整行", base, "20x:wq\r", "\nfghij\nklmno\n"},

		{"G 到末行", base, "Gx:wq\r", "abcde\nfghij\nlmno\n"},
		{"2G 到第二行", base, "2Gx:wq\r", "abcde\nghij\nklmno\n"},

		{"w 跨行到下一词首", base, "wx:wq\r", "abcde\nghij\nklmno\n"},
		{"w 停在末行行尾", base, "Gwi!\x1b:wq\r", "abcde\nfghij\nklmno!\n"},

		{"X 与 D 删除范围", base, "lXD:wq\r", "\nfghij\nklmno\n"},

		{"b 跨行回退", base, "jbx:wq\r", "bcde\nfghij\nklmno\n"},
		{"b 在首行行首", base, "bx:wq\r", "bcde\nfghij\nklmno\n"},

		{"b 跳过空行", "one\n\nx\n", "jjbx:wq\r", "ne\n\nx\n"},

		{"b 落到空行行首", "\nx\n", "jbiX\x1b:wq\r", "X\nx\n"},

		{"a 在行尾追加", "abc\n", "$aX\x1b:wq\r", "abcX\n"},

		{"方向键上与左", base, "\x1b[B\x1b[B\x1b[C\x1b[A\x1b[Dx:wq\r", "abcde\nghij\nklmno\n"},

		{"w 从空行跨到下一词", "\nx\n", "wx:wq\r", "\n\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			prepare(t, dir, "doc.txt", tc.body)
			run := runEditor(t, dir, []string{"doc.txt"}, []scriptStep{{keys: tc.keys}}, nil)
			if run.code != 0 {
				t.Fatalf("退出码 = %d，stderr=%q render=%q", run.code, run.stderr, run.render)
			}
			if got := content(t, dir, "doc.txt"); got != tc.want {
				t.Fatalf("内容 = %q，want %q", got, tc.want)
			}
		})
	}
}

func TestInsertAndRegisterCommandSurface(t *testing.T) {
	cases := []struct {
		name string
		body string
		keys string
		want string
	}{

		{"i 行首插入", "abc\n", "iX\x1b:wq\r", "Xabc\n"},
		{"a 光标后插入", "abc\n", "aX\x1b:wq\r", "aXbc\n"},

		{"A 行尾追加", "abc\n", "AX\x1b:wq\r", "abcX\n"},
		{"I 跳过空白插入", "  abc\n", "IX\x1b:wq\r", "  Xabc\n"},

		{"o 下方开行", "abc\n", "oX\x1b:wq\r", "abc\nX\n"},
		{"O 上方开行", "abc\n", "OX\x1b:wq\r", "X\nabc\n"},

		{"s 替换字符", "abc\n", "sX\x1b:wq\r", "Xbc\n"},
		{"S 替换整行", "abc\n", "SX\x1b:wq\r", "X\n\n"},

		{"x 与 p 粘在光标后", "abc\n", "xp:wq\r", "bac\n"},
		{"x 与 P 粘在光标前", "abc\n", "xP:wq\r", "abc\n"},

		{"dd 与 p 交换行", "abc\ndef\n", "ddp:wq\r", "def\nabc\n"},
		{"dd 与 P 粘回原位", "abc\ndef\n", "ddP:wq\r", "abc\ndef\n"},

		{"单行 dd 留下空行", "only\n", "dd:wq\r", "\n"},

		{"空寄存器粘贴", "ab\ncd\n", "$x0P:wq\r", "abcd\n"},

		{"u 撤销删除", "abc\n", "xu:wq\r", "abc\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			prepare(t, dir, "doc.txt", tc.body)
			run := runEditor(t, dir, []string{"doc.txt"}, []scriptStep{{keys: tc.keys}}, nil)
			if run.code != 0 {
				t.Fatalf("退出码 = %d，stderr=%q render=%q", run.code, run.stderr, run.render)
			}
			if got := content(t, dir, "doc.txt"); got != tc.want {
				t.Fatalf("内容 = %q，want %q", got, tc.want)
			}
		})
	}
}

func TestSearchCommandSurface(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		keys   string
		want   string
		status string
	}{

		{"模式内退格改写", "zz abcd\nac\n", "/ab\x08c\rx:wq\r", "zz abcd\nc\n", ""},

		{"空模式退格被忽略", "xyz\n", "/\x08y\rx:wq\r", "xz\n", ""},

		{"n 与 N 反向重复", "two\nzz two\n", "/two\rNxnx:wq\r", "wo\nzz wo\n", ""},

		{"反向搜索环绕", "aa\nbb\ncc\n", "?bb\rx:wq\r", "aa\nb\ncc\n", ""},

		{"环绕后整行搜索（向下）", "needle\nx\n", "j/needle\rx:wq\r", "eedle\nx\n", ""},

		{"环绕后整行搜索（向上）", "needle\nx\n", "j?needle\rx:wq\r", "eedle\nx\n", ""},

		{"没有上次搜索", "abc\n", "n:wq\r", "abc\n", "没有上次搜索"},

		{"找不到匹配", "abc\n", "/zzz\r:wq\r", "abc\n", `找不到 "zzz"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			prepare(t, dir, "doc.txt", tc.body)
			run := runEditor(t, dir, []string{"doc.txt"}, []scriptStep{{keys: tc.keys}}, nil)
			if run.code != 0 {
				t.Fatalf("退出码 = %d，stderr=%q render=%q", run.code, run.stderr, run.render)
			}
			if got := content(t, dir, "doc.txt"); got != tc.want {
				t.Fatalf("内容 = %q，want %q", got, tc.want)
			}
			if tc.status != "" && !strings.Contains(run.render, tc.status) {
				t.Fatalf("状态行缺少 %q", tc.status)
			}
		})
	}
}

func TestColonCommandSurface(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		keys   string
		want   string
		also   map[string]string
		status string
	}{

		{"干净缓冲退出", "abc\n", ":q\r", "abc\n", nil, ""},

		{"脏缓冲拒绝退出", "abc\n", "ix\x1b:q\r:q!\r", "abc\n", nil, "有未保存的修改（:q! 放弃）"},

		{"裸 w 写回", "abc\n", "ix\x1b:w\r:q\r", "xabc\n", nil, "已写入 doc.txt"},

		{"w 指定文件", "abc\n", ":w out.txt\r:q\r", "abc\n", map[string]string{"out.txt": "abc\n"}, "已写入 out.txt"},

		{"w 失败只报状态", "abc\n", ":w nodir/out.txt\r:q!\r", "abc\n", nil, "写 nodir/out.txt"},

		{"n 声明子集", "abc\n", ":n\r:n other.txt\r:q\r", "abc\n", nil, "多文件编辑是声明子集"},

		{"未知命令", "abc\n", ":bogus\r:q\r", "abc\n", nil, "不是编辑器命令: bogus"},

		{"数字定位", "abc\ndef\n", ":2\rx:wq\r", "abc\nef\n", nil, ""},

		{"0 行号夹取下界", "abc\ndef\n", ":0\rx:wq\r", "bc\ndef\n", nil, ""},
		{"99 行号夹取上界", "abc\ndef\n", ":99\rx:wq\r", "abc\nef\n", nil, ""},

		{"命令行退格", "abc\n", "ix\x1b:wq\x08\x08q!\r", "abc\n", nil, ""},

		{"命令行 ESC 取消", "abc\n", "ix\x1b:wq\x1b:q!\r", "abc\n", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			prepare(t, dir, "doc.txt", tc.body)
			run := runEditor(t, dir, []string{"doc.txt"}, []scriptStep{{keys: tc.keys}}, nil)
			if run.code != 0 {
				t.Fatalf("退出码 = %d，stderr=%q render=%q", run.code, run.stderr, run.render)
			}
			if got := content(t, dir, "doc.txt"); got != tc.want {
				t.Fatalf("内容 = %q，want %q", got, tc.want)
			}
			for name, want := range tc.also {
				if got := content(t, dir, name); got != want {
					t.Fatalf("%s = %q，want %q", name, got, want)
				}
			}
			if tc.status != "" && !strings.Contains(run.render, tc.status) {
				t.Fatalf("状态行缺少 %q", tc.status)
			}
		})
	}
}

func TestColonLineReadFailureKeepsBuffer(t *testing.T) {
	dir := t.TempDir()
	prepare(t, dir, "doc.txt", "abc\n")
	run := runEditor(t, dir, []string{"doc.txt"}, []scriptStep{{keys: ":wq"}, {fail: io.EOF}}, nil)
	if run.code != 1 {
		t.Fatalf("读取失败应以失败退出: code=%d stderr=%q", run.code, run.stderr)
	}
	if got := content(t, dir, "doc.txt"); got != "abc\n" {
		t.Fatalf("未完成的 :wq 不应写回: %q", got)
	}
	if !strings.Contains(run.stderr, "EOF") {
		t.Fatalf("退出诊断 = %q", run.stderr)
	}
}

func TestWriteWithoutNameKeepsBuffer(t *testing.T) {
	dir := t.TempDir()
	run := runEditor(t, dir, nil, []scriptStep{{keys: "iX\x1b:w\r:q!\r"}}, nil)
	if run.code != 0 {
		t.Fatalf("退出码 = %d，stderr=%q", run.code, run.stderr)
	}
	if !strings.Contains(run.render, "没有文件名") {
		t.Fatalf("状态行缺少未命名提示: %q", run.render)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("未命名缓冲不应该写出文件: %v", entries)
	}
}

func TestIdleAndCancelledInputIdentity(t *testing.T) {

	e := newTestEditor([]string{"abc", "def"}, []scriptStep{{idle: true}, {keys: "\x1b[Bx"}})
	if err := e.loop(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("脚本耗尽应上抛 EOF: %v", err)
	}
	if e.row != 1 || e.col != 0 || e.lines[1] != "ef" {
		t.Fatalf("空读后的按键未生效: lines=%q 光标=(%d,%d)", e.lines, e.row, e.col)
	}

	cancelled := newTestEditor([]string{"abc"}, []scriptStep{{fail: errno.Cancelled}})
	if err := cancelled.loop(context.Background()); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("取消身份必须原样上抛: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := newTestEditor([]string{"abc"}, nil).loop(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消上下文: %v", err)
	}
}

func TestReadEscapeSequenceShapes(t *testing.T) {
	cases := []struct {
		name  string
		steps []scriptStep
		want  string
		ok    bool
	}{
		{"完整 CSI", []scriptStep{{keys: "[A"}}, "\x1b[A", true},
		{"非 CSI 序列", []scriptStep{{keys: "Ox"}}, "", false},
		{"超过上限没有终字节", []scriptStep{{keys: "[1234567"}}, "", false},
		{"空读后继续", []scriptStep{{idle: true}, {keys: "[D"}}, "\x1b[D", true},
		{"读取失败", []scriptStep{{fail: io.EOF}}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEditor([]string{"abc"}, tc.steps)
			got, ok := e.readEscape(context.Background(), []byte{keyESC})
			if ok != tc.ok || string(got) != tc.want {
				t.Fatalf("readEscape = %q, %v；want %q, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestReadOneWithoutPendingByte(t *testing.T) {
	e := newTestEditor([]string{"abc"}, nil)
	if e.commandKey([]byte{'d'}) {
		t.Fatal("d 不是退出命令")
	}
	if len(e.lines) != 1 || e.lines[0] != "abc" {
		t.Fatalf("第二键缺失不应删除: %q", e.lines)
	}
}

func TestFirstNonBlankAndWordClass(t *testing.T) {
	for _, line := range []string{"", "  \t "} {
		if got := firstNonBlank(line); got != 0 {
			t.Fatalf("firstNonBlank(%q) = %d", line, got)
		}
	}
	if wordClass(' ') != 0 || wordClass('\t') != 0 {
		t.Fatal("空白属第 0 类")
	}
	for _, b := range []byte{'a', 'Z', '7', '_'} {
		if wordClass(b) != 1 {
			t.Fatalf("wordClass(%q) = %d，want 1", b, wordClass(b))
		}
	}
	for _, b := range []byte{'.', '-', '/'} {
		if wordClass(b) != 2 {
			t.Fatalf("wordClass(%q) = %d，want 2", b, wordClass(b))
		}
	}
}

func TestReadInsertionEndsOnEOF(t *testing.T) {
	e := newTestEditor([]string{"ab"}, []scriptStep{{keys: "X"}})
	e.insertMode(false)
	if len(e.lines) != 1 || e.lines[0] != "Xab" || e.col != 1 {
		t.Fatalf("EOF 前的插入必须保留: lines=%q 光标列=%d", e.lines, e.col)
	}
}

func TestSearchWithoutTerminatorKeepsLastPattern(t *testing.T) {
	e := newTestEditor([]string{"abc"}, []scriptStep{{keys: "b"}})
	e.search(1)
	if e.lastSearch != "" || e.row != 0 || e.col != 0 {
		t.Fatalf("未终止的搜索不得生效: 模式=%q 光标=(%d,%d)", e.lastSearch, e.row, e.col)
	}

	backspaced := newTestEditor([]string{"abc"}, []scriptStep{{keys: "\x08a\r"}})
	backspaced.search(1)
	if backspaced.lastSearch != "a" {
		t.Fatalf("空模式退格后被忽略的模式 = %q，want a", backspaced.lastSearch)
	}
}

type stubFile struct {
	vmfs.File
	data string
	err  error
}

func (f *stubFile) Read(p []byte) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	n := copy(p, f.data)
	f.data = f.data[n:]
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

type hugeFile struct {
	vmfs.File
	reads int
}

func (f *hugeFile) Read(p []byte) (int, error) {
	if f.reads >= 1030 {
		return 0, io.ErrClosedPipe
	}
	f.reads++
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func (f *hugeFile) Close() error { return nil }

func TestReadAllStopsOnBudgetErrorAndCancellation(t *testing.T) {
	if _, err := readAll(context.Background(), &hugeFile{}); err == nil || !strings.Contains(err.Error(), "4 MiB") {
		t.Fatalf("超预算必须报错: %v", err)
	}
	if _, err := readAll(context.Background(), &stubFile{err: fs.ErrPermission}); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("读错误必须原样上抛: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data, err := readAll(ctx, &stubFile{data: "abc"})
	if !errors.Is(err, context.Canceled) || data != "abc" {
		t.Fatalf("取消: data=%q err=%v", data, err)
	}
}

func TestNewSessionWindowFloorAndPlainReader(t *testing.T) {
	dir := t.TempDir()
	e, err := newSession(context.Background(), platformFS(dir), "missing.txt", 2, 4, strings.NewReader("k"), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if e.rows != 4 || e.cols != 8 {
		t.Fatalf("窗口下限 = %dx%d，want 4x8", e.rows, e.cols)
	}
	if e.status != "[新文件] missing.txt" {
		t.Fatalf("缺失文件提示 = %q", e.status)
	}
	if _, ok := e.input.(plainReader); !ok {
		t.Fatalf("普通读取者必须回退: %T", e.input)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.input.ReadContext(ctx, make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消必须在读之前生效: %v", err)
	}
}

type faultFS struct {
	vmfs.System
	writeOpenErr error
	readErr      error
	writeErr     error
	closeErr     error
	loadCloseErr error
	renameErr    error
}

func (f faultFS) Open(ctx context.Context, name string, flags uint32, mode fs.FileMode) (vmfs.File, error) {
	write := flags&(vmfs.WriteOnly|vmfs.ReadWrite) != 0
	if f.writeOpenErr != nil && write {
		return nil, f.writeOpenErr
	}
	file, err := f.System.Open(ctx, name, flags, mode)
	if err != nil {
		return nil, err
	}

	if write {
		return faultFile{File: file, writeErr: f.writeErr, closeErr: f.closeErr}, nil
	}
	return faultFile{File: file, readErr: f.readErr, closeErr: f.loadCloseErr}, nil
}

func (f faultFS) Rename(ctx context.Context, from, to string) error {
	if f.renameErr != nil {
		return f.renameErr
	}
	return f.System.Rename(ctx, from, to)
}

type faultFile struct {
	vmfs.File
	readErr  error
	writeErr error
	closeErr error
}

func (f faultFile) Read(p []byte) (int, error) {
	if f.readErr != nil {
		return 0, f.readErr
	}
	return f.File.Read(p)
}

func (f faultFile) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.File.Write(p)
}

func (f faultFile) Close() error {
	err := f.File.Close()
	if f.closeErr != nil {
		return f.closeErr
	}
	return err
}

func TestWriteFailurePathsReportStatusAndKeepFile(t *testing.T) {
	cases := []struct {
		name      string
		writeOpen error
		writeErr  error
		closeErr  error
		renameErr error
	}{
		{"打开临时文件失败", fs.ErrPermission, nil, nil, nil},
		{"写入失败", nil, io.ErrClosedPipe, nil, nil},
		{"关闭失败", nil, nil, io.ErrClosedPipe, nil},
		{"改名失败", nil, nil, nil, fs.ErrPermission},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			prepare(t, dir, "doc.txt", "abc\n")
			fault := faultFS{writeOpenErr: tc.writeOpen, writeErr: tc.writeErr, closeErr: tc.closeErr, renameErr: tc.renameErr}
			run := runEditor(t, dir, []string{"doc.txt"}, []scriptStep{{keys: "ix\x1b:w\r:q!\r"}}, func(set *capability.Set) {
				fault.System = set.FS
				set.FS = fault
			})
			if run.code != 0 {
				t.Fatalf("写失败不应中断会话: code=%d stderr=%q render=%q", run.code, run.stderr, run.render)
			}
			if got := content(t, dir, "doc.txt"); got != "abc\n" {
				t.Fatalf("写失败不得改动原文件: %q", got)
			}
			if !strings.Contains(run.render, "写 doc.txt") {
				t.Fatalf("状态行缺少写失败诊断: %q", run.render)
			}

			if tc.renameErr != nil {
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 1 || entries[0].Name() != "doc.txt" {
					t.Fatalf("改名失败必须清理临时文件: %v", entries)
				}
			}
		})
	}
}

func TestReadFailureAbortsSession(t *testing.T) {
	cases := []struct {
		name    string
		readErr error
		close   error
		want    string
	}{
		{"读失败", fs.ErrPermission, nil, fs.ErrPermission.Error()},
		{"关闭失败", nil, io.ErrClosedPipe, io.ErrClosedPipe.Error()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			prepare(t, dir, "doc.txt", "abc\n")
			fault := faultFS{readErr: tc.readErr, loadCloseErr: tc.close}
			run := runEditor(t, dir, []string{"doc.txt"}, nil, func(set *capability.Set) {
				fault.System = set.FS
				set.FS = fault
			})
			if run.code != 1 {
				t.Fatalf("装载失败必须以 1 退出: code=%d stderr=%q", run.code, run.stderr)
			}
			if !strings.Contains(run.stderr, tc.want) {
				t.Fatalf("诊断 %q 缺少 %q", run.stderr, tc.want)
			}
			if len(run.states) != 0 {
				t.Fatalf("会话未开始不得切换终端行规程: %+v", run.states)
			}
		})
	}
}

type descriptorsWithoutSetter struct{ fd.Descriptors }

type failingTerminalSetter struct{ fd.Descriptors }

func (failingTerminalSetter) SetTerminalState(context.Context, uint64, uint32, fd.TerminalState) error {
	return errno.NotSupported
}

func TestStartupCapabilityAndArgumentFailures(t *testing.T) {
	t.Run("参数解析错误", func(t *testing.T) {
		dir := t.TempDir()
		run := runEditor(t, dir, []string{"--bogus"}, nil, nil)
		if run.code != 2 || !strings.Contains(run.stderr, "参数错误") {
			t.Fatalf("参数错误: code=%d stderr=%q", run.code, run.stderr)
		}
	})
	t.Run("多文件只编辑第一个", func(t *testing.T) {
		dir := t.TempDir()
		prepare(t, dir, "a.txt", "abc\n")
		prepare(t, dir, "b.txt", "zzz\n")
		run := runEditor(t, dir, []string{"a.txt", "b.txt"}, []scriptStep{{keys: "ix\x1b:wq\r"}}, nil)
		if run.code != 0 {
			t.Fatalf("退出码 = %d，stderr=%q", run.code, run.stderr)
		}
		if !strings.Contains(run.stderr, "只编辑 a.txt") {
			t.Fatalf("多文件提示 = %q", run.stderr)
		}
		if got := content(t, dir, "a.txt"); got != "xabc\n" {
			t.Fatalf("第一个文件 = %q", got)
		}
		if got := content(t, dir, "b.txt"); got != "zzz\n" {
			t.Fatalf("第二个文件不应被编辑: %q", got)
		}
	})
	t.Run("没有文件系统", func(t *testing.T) {
		dir := t.TempDir()
		run := runEditor(t, dir, []string{"doc.txt"}, nil, func(set *capability.Set) { set.FS = nil })

		if run.code != 2 || !strings.Contains(run.stderr, "vi: ") {
			t.Fatalf("缺文件系统: code=%d stderr=%q", run.code, run.stderr)
		}
	})
	t.Run("没有终端设置能力", func(t *testing.T) {
		dir := t.TempDir()
		run := runEditor(t, dir, []string{"doc.txt"}, nil, func(set *capability.Set) {
			set.FD = descriptorsWithoutSetter{set.FD}
		})
		if run.code != 1 || !strings.Contains(run.stderr, "没有终端设置能力") {
			t.Fatalf("缺 TerminalSetter: code=%d stderr=%q", run.code, run.stderr)
		}
	})
	t.Run("终端设置失败", func(t *testing.T) {
		dir := t.TempDir()
		run := runEditor(t, dir, []string{"doc.txt"}, nil, func(set *capability.Set) {
			set.FD = failingTerminalSetter{set.FD}
		})
		if run.code != 1 || !strings.Contains(run.stderr, "切换终端模式失败") {
			t.Fatalf("设置失败: code=%d stderr=%q", run.code, run.stderr)
		}
	})
}

func TestMissingProcessAndDescriptorViews(t *testing.T) {
	dir := t.TempDir()
	if err := runInterp(t, context.Background(), dir, nil); err == nil {
		t.Fatal("没有进程视图必须失败")
	} else {
		var status interp.ExitStatus
		if !errors.As(err, &status) || int(status) != 2 {
			t.Fatalf("没有进程视图: %v", err)
		}
	}
	process := proc.Proc{ID: 1, Dir: dir, Resources: capability.Set{}, SID: 1, PGID: 1}
	if err := invokeProcess(t, dir, nil, process); err == nil {
		t.Fatal("没有描述符必须失败")
	} else {
		var status interp.ExitStatus
		if !errors.As(err, &status) || int(status) != 2 {
			t.Fatalf("没有描述符: %v", err)
		}
	}
}

func TestCancelledInputIsReportedAsCancellation(t *testing.T) {
	dir := t.TempDir()
	prepare(t, dir, "doc.txt", "abc\n")
	run := runEditor(t, dir, []string{"doc.txt"}, []scriptStep{{fail: errno.Cancelled}}, nil)
	if run.code != 2 {
		t.Fatalf("取消身份应走命令层退出码 2: code=%d stderr=%q", run.code, run.stderr)
	}
	if !strings.Contains(run.stderr, "取消") {
		t.Fatalf("取消诊断 = %q", run.stderr)
	}

	eof := runEditor(t, dir, []string{"doc.txt"}, []scriptStep{{keys: ":q"}, {fail: io.EOF}}, nil)
	if eof.code != 1 || !strings.Contains(eof.stderr, "EOF") {
		t.Fatalf("EOF 应走编辑器失败路径: code=%d stderr=%q", eof.code, eof.stderr)
	}
}

func (f *fixture) waitScreen(want func([]string) bool) []string {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		lines := f.screen()
		if want(lines) {
			return lines
		}
		time.Sleep(time.Millisecond)
	}
	f.cancel()
	f.t.Fatalf("屏幕未达到期望状态: %q", f.screen())
	return nil
}

func trimRow(line string) string { return strings.TrimRight(line, " ") }

func TestScreenWindowFollowsCursor(t *testing.T) {
	f := newFixture(t)
	var body strings.Builder
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&body, "l%d\n", i)
	}
	prepare(t, f.dir, "doc.txt", body.String())
	done := f.runVI("doc.txt")

	f.waitScreen(func(lines []string) bool { return trimRow(lines[0]) == "l1" })

	f.sendKeys(strings.Repeat("j", 7))
	f.waitScreen(func(lines []string) bool { return trimRow(lines[0]) == "l4" })

	f.sendKeys("G")
	lines := f.waitScreen(func(lines []string) bool { return trimRow(lines[0]) == "l8" })
	if trimRow(lines[4]) != "l12" {
		t.Fatalf("末行窗口 = %q", lines)
	}
	if !strings.Contains(lines[5], "doc.txt") || !strings.Contains(lines[5], "12 行") {
		t.Fatalf("状态行 = %q", lines[5])
	}

	f.sendKeys("1G")
	f.waitScreen(func(lines []string) bool { return trimRow(lines[0]) == "l1" })

	f.sendKeys(":8\r")
	f.waitScreen(func(lines []string) bool { return trimRow(lines[0]) == "l4" })

	f.sendKeys("/l12\r")
	f.waitScreen(func(lines []string) bool { return trimRow(lines[0]) == "l8" })

	f.sendKeys(":0\r")
	f.waitScreen(func(lines []string) bool { return trimRow(lines[0]) == "l1" })
	f.sendKeys(":q!\r")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("退出: %v", err)
		}
	case <-time.After(5 * time.Second):
		f.cancel()
		<-done
		t.Fatal("vi 未退出（已取消会话）")
	}
}
