package main

import (
	"bytes"
	"encoding/json/v2"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, root, name, text string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestRustArgumentsAndGoldenContracts(t *testing.T) {
	args, err := rustArguments(`"-n", r#"a"b"#, "x\\y", LINES1, LINES2, NO_NEW_LINE,`)
	if err != nil || strings.Join(args, "|") != "-n|a\"b|x\\y|input/lines1|input/lines2|input/no-new-line.txt" {
		t.Fatal(args, err)
	}
	for _, bad := range []string{`"unclosed`, `"a\u{1234}"`, `r#"unclosed`, `rBAD"x"BAD`, `"x" bad`, `expression()`} {
		if _, err := rustArguments(bad); err == nil {
			t.Fatal(bad)
		}
	}
	if shellQuote("a'b") != "'a'\"'\"'b'" || sortOutput("b\na\n") != "a\nb" {
		t.Fatal("参数引用或无序输出比较失效")
	}
	dir := t.TempDir()
	fixture(t, dir, "testdata/gawk/ok.awk", "BEGIN{print 1}")
	fixture(t, dir, "testdata/gawk/ok.ok", "1\n")
	fixture(t, dir, "testdata/gawk/hex2.awk", "")
	fixture(t, dir, "testdata/gawk/hex2.ok", "")
	fixture(t, dir, "testdata/gawk/error.awk", "")
	fixture(t, dir, "testdata/gawk/error.ok", "error.awk: syntax")
	fixture(t, dir, "testdata/gawk/noexpected.awk", "")
	cases, err := loadUpstream("goawk", dir)
	if err != nil || len(cases) != 4 {
		t.Fatal(cases, err)
	}
	for _, c := range cases {
		if c.ID == "ok" && (c.Blocked != "" || c.Script != "awk -f program.awk < stdin.txt" || c.Files["stdin.txt"] != "") {
			t.Fatal(c)
		}
		if c.ID != "ok" && c.Blocked == "" {
			t.Fatal(c)
		}
	}
	fixture(t, dir, "sh/tests/sh/ok.sh", "echo ok")
	fixture(t, dir, "sh/tests/sh/ok.out", "ok\n")
	fixture(t, dir, "sh/tests/sh/checker.sh", "echo ok")
	fixture(t, dir, "sh/tests/sh/checker.out", "ok\n")
	fixture(t, dir, "sh/tests/integration.rs", `test_script(include_str!("sh/ok.sh"), include_str!("sh/ok.out"));`)
	cases, err = loadUpstream("posixutils", dir)
	if err != nil || len(cases) != 2 || cases[0].Blocked == "" || cases[1].Blocked != "" {
		t.Fatal(cases, err)
	}
	fixture(t, dir, "tests/by-util/test_sed.rs", `check_output!(foo,["-n", "1p", LINES1]); check_output!(bar,[expression()]);`)
	fixture(t, dir, "tests/fixtures/sed/output/foo", "line\n")
	fixture(t, dir, "tests/fixtures/sed/output/bar", "")
	cases, err = loadUpstream("sed", dir)
	if err != nil || len(cases) != 2 || cases[0].Script != "sed '-n' '1p' 'input/lines1'" || cases[1].Blocked == "" {
		t.Fatal(cases, err)
	}
	if _, err := loadUpstream("unknown", dir); err == nil {
		t.Fatal("未知适配器")
	}
	if _, err := loadUpstream("sed", t.TempDir()); err == nil {
		t.Fatal("缺少上游文件")
	}
	if _, err := loadUpstream("goawk", t.TempDir()); err == nil {
		t.Fatal("缺少 golden 目录")
	}
}
func gitTest(t *testing.T, path string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal(string(out), err)
	}
	return strings.TrimSpace(string(out))
}
func TestPinnedSourceFetchAndActualWASIUpstream(t *testing.T) {
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("GNU vector 适配需要 Perl")
	}
	remote := t.TempDir()
	gitTest(t, remote, "init", "-q")
	gitTest(t, remote, "config", "user.name", "test")
	gitTest(t, remote, "config", "user.email", "test@example.invalid")
	fixture(t, remote, "sh/tests/sh/ok.sh", "printf 'verified\\n'")
	fixture(t, remote, "sh/tests/sh/ok.out", "verified\n")
	fixture(t, remote, "sh/tests/sh/fail.sh", "unknown_test_command")
	fixture(t, remote, "sh/tests/sh/fail.out", "expected\n")
	fixture(t, remote, "sh/tests/sh/blocked.sh", "echo custom")
	fixture(t, remote, "sh/tests/sh/blocked.out", "custom\n")
	fixture(t, remote, "sh/tests/integration.rs", `test_script(include_str!("sh/ok.sh"), include_str!("sh/ok.out")); test_script(include_str!("sh/fail.sh"), include_str!("sh/fail.out"));`)
	gitTest(t, remote, "add", ".")
	gitTest(t, remote, "commit", "-qm", "fixture")
	commit := gitTest(t, remote, "rev-parse", "HEAD")
	saved := upstreamJSON
	t.Cleanup(func() { upstreamJSON = saved })
	upstreamJSON, _ = json.Marshal([]upstreamSource{{Name: "posixutils", URL: remote, Commit: commit, Scope: "fixture"}})
	dir := filepath.Join(t.TempDir(), "sources")
	if err := fetchSources(t.Context(), dir, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := fetchSources(t.Context(), dir, io.Discard); err != nil {
		t.Fatal(err)
	}
	var console bytes.Buffer
	out := filepath.Join(t.TempDir(), "report")
	if err := runUpstream(t.Context(), dir, out, "", false, &console); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(console.String(), "FAIL") || !strings.Contains(console.String(), "BLOCKED") || !strings.Contains(console.String(), "PASS") {
		t.Fatal(console.String())
	}
	if err := runUpstream(t.Context(), dir, out, "", true, io.Discard); err == nil {
		t.Fatal("失败与适配缺口未阻止 gate")
	}
	if err := runUpstream(t.Context(), dir, out, "missing", false, io.Discard); err == nil {
		t.Fatal("空筛选冒充成功")
	}
	if err := runUpstream(t.Context(), dir, out, "posixutils/ok", true, io.Discard); err != nil {
		t.Fatal(err)
	}
	fixture(t, filepath.Join(dir, "posixutils"), "sh/tests/sh/ok.sh", "exit 99")
	all, _ := sources()
	if err := verifySource(t.Context(), filepath.Join(dir, "posixutils"), all[0]); err == nil {
		t.Fatal("修改过的源码冒充固定版本")
	}
	gitTest(t, filepath.Join(dir, "posixutils"), "checkout", "--", ".")
	if err := runUpstream(t.Context(), dir, out, "", false, brokenWriter{}); err == nil {
		t.Fatal("丢弃报告输出错误")
	}
	upstreamJSON, _ = json.Marshal([]upstreamSource{{Name: "posixutils", URL: remote, Commit: "bad"}})
	if err := fetchSources(t.Context(), dir, io.Discard); err == nil {
		t.Fatal("接受错误 commit")
	}
	upstreamJSON = []byte("bad")
	if _, err := sources(); err == nil {
		t.Fatal("接受损坏 manifest")
	}
	if err := runUpstream(t.Context(), dir, out, "", false, io.Discard); err == nil {
		t.Fatal("接受损坏 manifest")
	}
	if err := fetchSources(t.Context(), dir, io.Discard); err == nil {
		t.Fatal("接受损坏 manifest")
	}
}
func TestCoreutilsVectorAndBinaryReport(t *testing.T) {
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("需要 Perl")
	}
	dir := t.TempDir()
	for _, cmd := range []string{"basename", "dirname"} {
		fixture(t, dir, "tests/misc/"+cmd+".pl", `use strict; my @t=(["ok", "'a b'", {OUT=>"a b\n"}, {ENV=>"LC_ALL=C"}], ["error",{ERR=>"bad"},{EXIT=>"1"}],["unknown",{MAGIC=>"x"}]); exit run_tests("test", "`+cmd+`", \@t);`)
	}
	cases, err := loadCoreutils(dir)
	if err != nil || len(cases) != 6 || cases[0].Script != "LC_ALL=C basename 'a b'" || cases[1].Blocked == "" || cases[2].Blocked == "" {
		t.Fatal(cases, err)
	}
	fixture(t, dir, "tests/misc/basename.pl", `print "bad";`)
	if _, err := loadCoreutils(dir); err == nil {
		t.Fatal("接受损坏 vector 输出")
	}
	rr := upstreamResult{upstreamCase: upstreamCase{ID: "binary", Expected: string([]byte{0xff}), Files: map[string]string{"file": string([]byte{0xfe})}}, Stdout: string([]byte{0xff}), Status: "PASS"}
	if err := writeUpstreamReport(filepath.Join(dir, "out"), upstreamReport{Results: []upstreamResult{rr}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "out/report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed upstreamReport
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(parsed.Results[0].RawBytes["stdout"], []byte{0xff}) || !bytes.Equal(parsed.Results[0].RawBytes["file:file"], []byte{0xfe}) {
		t.Fatal("原始字节丢失", string(raw))
	}
	fixture(t, dir, "blocked", "")
	if err := writeUpstreamReport(filepath.Join(dir, "blocked"), upstreamReport{}); err == nil {
		t.Fatal("忽略无效报告目录")
	}
}
