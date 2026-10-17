package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestActualWASIBaselineAndCompletionReport(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	var console bytes.Buffer
	if err := execute(t.Context(), root, out, "", "", "", "assistant-wasi", false, &console); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(out, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var r report
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	if r.Standard != "POSIX.1-2016" || len(r.PartialEntries)+len(r.MissingEntries) != 160 || len(r.Results) != len(probes()) || r.WASMSHA256 == "" {
		t.Fatal(r)
	}
	seen := map[string]result{}
	for _, rr := range r.Results {
		seen[rr.ID] = rr
	}

	for id, rr := range seen {
		if rr.Status != "PASS" {
			t.Fatalf("开发样本 %s 未通过: %+v", id, rr)
		}
	}

	if rr := seen["utility.env"]; rr.Exit != 0 || rr.Stdout != "value" {
		t.Fatal(rr)
	}
	if r.Journal != nil || !strings.Contains(r.Official["VSX-PCTS2016"], "未运行") {
		t.Fatal("将开发结果冒充官方结果")
	}
	md, _ := os.ReadFile(filepath.Join(out, "report.md"))
	if !bytes.Contains(md, []byte("grep.basic-regexp-plus")) || !bytes.Contains(md, []byte("未运行")) {
		t.Fatal(string(md))
	}
}

func TestEvaluationAndMetadataFailures(t *testing.T) {
	p := probe{ID: "case", Stdout: "ok", Exit: 0}
	for _, test := range []struct {
		code int
		text string
		err  error
		want string
	}{{0, "ok", nil, "PASS"}, {1, "ok", nil, "FAIL"}, {0, "different", nil, "FAIL"}, {0, "ok", io.ErrClosedPipe, "FAIL"}} {
		r := evaluate(p, test.code, test.text, "diagnostic", test.err)
		if r.Status != test.want || r.Stderr != "diagnostic" {
			t.Fatal(r)
		}
	}
	for _, test := range []struct{ journal, suite, version, target string }{
		{"none", "bad", "3.1", "assistant-wasi"},
		{"none", "VSC-PCTS2016", "", "assistant-wasi"},
		{"none", "VSC-PCTS2016", "3.1", ""},
		{"none", "VSC-PCTS2016", "1", "assistant-wasi"},
		{"none", "VSC-PCTS2016", "3.1", "assistant-wasi"},
		{"", "", "", "host-linux"},
	} {
		if err := execute(t.Context(), "missing", t.TempDir(), test.journal, test.suite, test.version, test.target, false, io.Discard); err == nil {
			t.Fatal(test)
		}
	}
	if err := execute(t.Context(), "missing", t.TempDir(), "", "", "", "assistant-wasi", false, io.Discard); err == nil {
		t.Fatal("未找到源注册表却统计入口")
	}
}
func TestSourceIndexAndReportFailures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.go")
	for _, text := range []string{"broken", "package commands; func Other(){}", "package commands; func Registry()map[int]int{return map[int]int{1:2}}"} {
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Error(err)
		}
		if _, err := sourceEntries(path); err == nil {
			t.Fatal(text)
		}
	}
	if err := os.WriteFile(path, []byte(`package commands; func Registry()map[string]int{return map[string]int{"wc":1,"cat":2}}`), 0600); err != nil {
		t.Error(err)
	}
	entries, err := sourceEntries(path)
	if err != nil || strings.Join(entries, " ") != "cat wc" {
		t.Fatal(entries, err)
	}
	if err := writeReport(path, report{}); err == nil {
		t.Fatal("输出目录为普通文件")
	}
	r := report{Standard: "POSIX.1-2016", Journal: &journalReport{Counts: map[string]int{"PASS": 1}}}
	if err := writeReport(filepath.Join(dir, "out"), r); err != nil {
		t.Fatal(err)
	}
}
func TestJournalImportAndStrictGate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal")
	body := "0|start|TCC Start\n10|0 /tset/grep 00:00|TC Start\n220|0 1 0 00:01|PASS\n220|0 2 1 00:02|FAIL\n900|00:03|TCC End\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Error(err)
	}
	out := t.TempDir()
	var console bytes.Buffer
	if err := execute(t.Context(), "", out, path, "VSC-PCTS2016", "3.1", "assistant-wasi", false, &console); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(console.String(), "FAIL:1") || !strings.Contains(console.String(), "run_complete=true") {
		t.Fatal(console.String())
	}
	if err := execute(t.Context(), "", out, path, "VSC-PCTS2016", "3.1", "assistant-wasi", true, io.Discard); err == nil {
		t.Fatal("FAIL结果通过门禁")
	}
	if err := os.WriteFile(path, []byte(strings.Split(body, "900|")[0]), 0600); err != nil {
		t.Error(err)
	}
	if err := execute(t.Context(), "", out, path, "VSC-PCTS2016", "3.1", "assistant-wasi", true, io.Discard); err == nil {
		t.Fatal("截断journal通过门禁")
	}
	if err := execute(t.Context(), "", out, path, "assistant-posix-smoke", "development", "assistant-wasi", false, brokenWriter{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Error(err)
	}
	if err := execute(t.Context(), "", out, path, "VSC-PCTS2016", "3.1", "assistant-wasi", false, io.Discard); err == nil {
		t.Fatal("坏journal未拒绝")
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	root, _ := filepath.Abs("../..")
	if err := execute(ctx, root, t.TempDir(), "", "", "", "assistant-wasi", false, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
