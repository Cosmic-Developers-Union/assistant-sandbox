package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"

	_ "embed"
	"encoding/hex"
	"encoding/json/v2"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	sandbox "github.com/Cosmic-Developers-Union/assistant-sandbox"
)

//go:embed utility-index.json
var indexJSON []byte

//go:embed target.json
var targetJSON []byte

type certificationTarget struct {
	Standard      string        `json:"standard"`
	Specification string        `json:"specification"`
	SuiteSource   string        `json:"suite_source"`
	CheckedOn     string        `json:"checked_on"`
	Suites        []suiteTarget `json:"suites"`
}
type suiteTarget struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Domain    string `json:"domain"`
	Directory string `json:"directory"`
}

type utilityIndex struct {
	Standard  string   `json:"standard"`
	Source    string   `json:"source"`
	Utilities []string `json:"utilities"`
}
type probe struct {
	ID, Area, Script, Stdout, Source string
	Exit                             int
}
type result struct {
	ID, Area, Script, Stdout, Stderr, Expected, Source, Error, Status string
	Exit, ExpectedExit                                                int
}
type report struct {
	ConfiguredTarget certificationTarget `json:"configured_target"`
	Standard         string              `json:"standard"`
	Target           string              `json:"target"`
	Time             time.Time           `json:"time"`
	WASMSHA256       string              `json:"wasm_sha256,omitempty"`
	UtilitySource    string              `json:"utility_source,omitempty"`
	PartialEntries   []string            `json:"partial_entries,omitempty"`
	MissingEntries   []string            `json:"missing_entries,omitempty"`
	Official         map[string]string   `json:"official_suites"`
	Results          []result            `json:"development_checks,omitempty"`
	Journal          *journalReport      `json:"journal,omitempty"`
	Note             string              `json:"note"`
}

func main() {
	filter := flag.String("filter", "", "上游用例 ID 前缀，例如 coreutils/basename/ 或 posixutils/word_expansion/")
	upstream := flag.Bool("upstream", false, "运行固定上游测试子集")
	fetch := flag.Bool("fetch", false, "下载并核验固定上游源码")
	sourceDir := flag.String("sources", ".local/upstream", "上游源码目录")
	out := flag.String("out", ".generated", "私有结果目录")
	root := flag.String("root", "../..", "仓库根目录")
	journal := flag.String("journal", "", "只汇总已有 TET journal，不执行目标")
	suite := flag.String("suite", "", "journal 所属套件名称")
	version := flag.String("suite-version", "", "实际套件版本")
	target := flag.String("target", "assistant-wasi", "真实被测目标标识；不得把宿主结果标为沙箱结果")
	strict := flag.Bool("strict", false, "开发检查失败时返回非零")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var runErr error
	if *fetch {
		runErr = fetchSources(ctx, *sourceDir, os.Stdout)
	} else if *upstream {
		runErr = runUpstream(ctx, *sourceDir, *out, *filter, *strict, os.Stdout)
	} else {
		runErr = execute(ctx, *root, *out, *journal, *suite, *version, *target, *strict, os.Stdout)
	}
	if err := runErr; err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func execute(ctx context.Context, root, out, journal, suite, version, target string, strict bool, stdout io.Writer) (closeResult0 error) {
	var configured certificationTarget
	if err := json.Unmarshal(targetJSON, &configured, json.RejectUnknownMembers(true)); err != nil {
		return err
	}
	r := report{ConfiguredTarget: configured, Standard: "POSIX.1-2016", Target: target, Time: time.Now().UTC(), Official: map[string]string{"VSC-PCTS2016": "未运行：缺少授权套件与已验证的目标适配", "VSX-PCTS2016": "未运行：缺少授权套件，当前目标没有 POSIX C 系统接口运行入口"}, Note: "开发检查仅是自建样本。入口存在不代表全部参数或语义符合规范；索引包含可选与扩展 utilities，不能作为 mandatory 项的符合率。"}
	if journal != "" {
		if !slices.Contains([]string{"VSC-PCTS2016", "VSX-PCTS2016", "assistant-posix-smoke"}, suite) || version == "" || target == "" {
			return fmt.Errorf("导入须指定受支持的 suite、实际 suite-version 与真实 target")
		}
		for _, expected := range configured.Suites {
			if suite == expected.Name && version != expected.Version {
				return fmt.Errorf("套件版本%s与配置的%s不一致，请核实实际授权版本并更新target.json", version, expected.Version)
			}
		}
		f, err := os.Open(journal)
		if err != nil {
			return err
		}
		defer func() {
			closeResult0 = errors.Join(closeResult0, f.Close())
		}()

		summary, err := parseJournal(f)
		if err != nil {
			return err
		}
		summary.Suite = suite
		summary.Version = version
		summary.Target = target
		r.Journal = &summary
		r.Official = map[string]string{suite: "仅导入 journal；完整测试计划覆盖与认证资格尚未验证"}
	} else {
		if target != "assistant-wasi" {
			return fmt.Errorf("内置开发检查的目标固定为 assistant-wasi")
		}
		var index utilityIndex
		if err := json.Unmarshal(indexJSON, &index, json.RejectUnknownMembers(true)); err != nil {
			return err
		}
		registered, err := sourceEntries(filepath.Join(root, "userland/commands/registry.go"))
		if err != nil {
			return err
		}

		entries := strings.Fields(": true false echo printf test [ cd pwd set shift unset export readonly read getopts break continue return exit alias unalias command type hash eval . wait trap exec")
		entries = append(entries, registered...)
		for _, name := range index.Utilities {
			if slices.Contains(entries, name) {
				r.PartialEntries = append(r.PartialEntries, name)
			} else {
				r.MissingEntries = append(r.MissingEntries, name)
			}
		}
		r.UtilitySource = index.Source
		code, err := os.ReadFile(filepath.Join(root, "assistant-bash.wasm.gz"))
		if err != nil {
			return err
		}
		hash := sha256.Sum256(code)
		r.WASMSHA256 = hex.EncodeToString(hash[:])
		runtime, err := sandbox.New(ctx, sandbox.Config{ShellArchive: code, Timeout: 3 * time.Second})
		if err != nil {
			return err
		}
		defer func() {
			closeResult0 = errors.Join(closeResult0, runtime.Close())
		}()

		work, err := os.MkdirTemp("", "assistant-posix-check-*")
		if err != nil {
			return err
		}
		defer func() {
			closeResult0 = errors.Join(closeResult0, os.RemoveAll(work))
		}()

		for _, p := range probes() {
			if err := ctx.Err(); err != nil {
				return err
			}
			dir := filepath.Join(work, p.ID)
			if err := os.Mkdir(dir, 0700); err != nil {
				return err
			}
			session, err := runtime.Bind(dir)
			if err != nil {
				return err
			}
			var output, diagnostic bytes.Buffer
			actual, runErr := session.Execute(ctx, dir, p.Script, &output, &diagnostic)
			closeErr := session.Close()
			if closeErr != nil {
				return closeErr
			}
			rr := evaluate(p, actual.ExitCode, output.String(), diagnostic.String(), runErr)
			r.Results = append(r.Results, rr)
			if _, err := fmt.Fprintf(stdout, "%-4s %-30s exit=%d\n", rr.Status, rr.ID, rr.Exit); err != nil {
				return err
			}
		}
	}
	if err := writeReport(out, r); err != nil {
		return err
	}
	passed, failed := 0, 0
	for _, rr := range r.Results {
		if rr.Status == "PASS" {
			passed++
		} else {
			failed++
		}
	}
	if r.Journal != nil {
		summary := r.Journal
		if _, err := fmt.Fprintf(stdout, "TET journal: suite=%s version=%s target=%s run_complete=%t counts=%v；结果: %s/report.md\n", summary.Suite, summary.Version, summary.Target, summary.RunComplete, summary.Counts, out); err != nil {
			return err
		}
		for name, n := range summary.Counts {
			if name != "PASS" {
				failed += n
			}
		}
		if strict && !summary.RunComplete {
			return fmt.Errorf("journal 缺少 TCC End，运行未完成")
		}
	} else {
		if _, err := fmt.Fprintf(stdout, "开发样本: PASS=%d FAIL=%d；部分实现入口=%d，缺少入口=%d；结果: %s/report.md\n官方 VSC/VSX 尚未运行，开发样本不是认证结果。\n", passed, failed, len(r.PartialEntries), len(r.MissingEntries), out); err != nil {
			return err
		}
	}

	if strict && failed > 0 {
		return fmt.Errorf("%d 个结果不是 PASS", failed)
	}
	return nil
}

func evaluate(p probe, code int, output, diagnostic string, err error) result {
	r := result{ID: p.ID, Area: p.Area, Script: p.Script, Stdout: output, Stderr: diagnostic, Expected: p.Stdout, Source: p.Source, Exit: code, ExpectedExit: p.Exit, Status: "PASS"}
	if err != nil {
		r.Error = err.Error()
	}
	if err != nil || code != p.Exit || output != p.Stdout {
		r.Status = "FAIL"
	}
	return r
}

func sourceEntries(path string) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "Registry" {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			pair, ok := node.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			key, ok := pair.Key.(*ast.BasicLit)
			if !ok || key.Kind != token.STRING {
				return true
			}
			name, err := strconv.Unquote(key.Value)
			if err == nil {
				names = append(names, name)
			}
			return true
		})
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("未找到命令注册表，不能生成入口统计")
	}
	slices.Sort(names)
	return names, nil
}

func writeReport(dir string, r report) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(r, json.Deterministic(true))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), raw, 0600); err != nil {
		return err
	}
	var md strings.Builder
	fmt.Fprintf(&md, "# POSIX.1-2016 开发检查\n\n目标: %s · 时间 %s\n\n%s\n\nWASM gzip SHA-256: %s\n\n", r.Target, r.Time.Format(time.RFC3339), r.Note, r.WASMSHA256)
	for _, name := range slices.Sorted(maps.Keys(r.Official)) {
		fmt.Fprintf(&md, "- %s: %s\n", name, r.Official[name])
	}
	if r.Journal != nil {
		j := r.Journal
		fmt.Fprintf(&md, "\n## TET journal 汇总\n\n套件 %s · 版本 %s · 目标 %s · 运行完成 %t\n\n计划覆盖：%s\n\n", j.Suite, j.Version, j.Target, j.RunComplete, j.CampaignCoverage)
		for _, name := range slices.Sorted(maps.Keys(j.Counts)) {
			fmt.Fprintf(&md, "- %s: %d\n", name, j.Counts[name])
		}
		for _, tp := range j.Results {
			fmt.Fprintf(&md, "\n- %s · activity=%d TP=%d · %s (code=%d) · 原文 %q\n", tp.Case, tp.Activity, tp.TP, tp.Result, tp.Code, tp.Description)
		}
	}
	if r.UtilitySource != "" {
		fmt.Fprintf(&md, "\n## 入口缺口\n\n索引来源: %s\n\n部分实现入口 %d 个，缺少入口 %d 个。不表示完整符合率。\n\n缺少入口:\n\n%s\n\n", r.UtilitySource, len(r.PartialEntries), len(r.MissingEntries), strings.Join(r.MissingEntries, ", "))
	}
	for _, rr := range r.Results {
		fmt.Fprintf(&md, "## %s · %s\n\n规范: %s\n\n脚本: %s\n\n实际退出码 %d，期望 %d。\n\n实际 stdout: %q\n\n期望 stdout: %q\n\nstderr: %q\n\n运行时错误: %q\n\n", rr.ID, rr.Status, rr.Source, rr.Script, rr.Exit, rr.ExpectedExit, rr.Stdout, rr.Expected, rr.Stderr, rr.Error)
	}
	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(md.String()), 0600)
}
