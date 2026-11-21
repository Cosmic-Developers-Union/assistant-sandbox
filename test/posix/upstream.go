package main

import (
	"bytes"
	"context"
	"errors"

	_ "embed"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	sandbox "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/legacy"
)

//go:embed upstream.json
var upstreamJSON []byte

type upstreamSource struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Commit string `json:"commit"`
	Scope  string `json:"scope"`
}
type upstreamCase struct {
	ID, Script, Expected, Stderr, Blocked, Source, Fixture string
	Files                                                  map[string]string
	Sort                                                   bool
}
type upstreamResult struct {
	Executed bool              `json:"executed"`
	RawBytes map[string][]byte `json:"raw_bytes,omitempty"`
	upstreamCase
	Status, Stdout, Diagnostic, Error string
	Exit                              int
}
type upstreamReport struct {
	Filter  string
	Target  string
	Time    time.Time
	Sources []upstreamSource
	Counts  map[string]int
	Results []upstreamResult
	Note    string
}

func sources() ([]upstreamSource, error) {
	var s []upstreamSource
	err := json.Unmarshal(upstreamJSON, &s, json.RejectUnknownMembers(true))
	return s, err
}

func verifySource(ctx context.Context, dir string, source upstreamSource) error {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return fmt.Errorf("%s 尚未下载；运行 make sources: %w", source.Name, err)
	}
	if strings.TrimSpace(string(out)) != source.Commit {
		return fmt.Errorf("%s commit 不匹配固定版本", source.Name)
	}
	out, err = exec.CommandContext(ctx, "git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		return err
	}
	if len(out) != 0 {
		return fmt.Errorf("%s 源码有本地修改，无法标为固定上游测试", source.Name)
	}
	return nil
}

func fetchSources(ctx context.Context, dir string, stdout io.Writer) error {
	all, err := sources()
	if err != nil {
		return err
	}
	for _, s := range all {
		path := filepath.Join(dir, s.Name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := os.MkdirAll(path, 0700); err != nil {
				return err
			}
			for _, args := range [][]string{{"init", path}, {"-C", path, "remote", "add", "origin", s.URL}, {"-C", path, "fetch", "--depth", "1", "origin", s.Commit}, {"-C", path, "checkout", "--detach", "FETCH_HEAD"}} {
				cmd := exec.CommandContext(ctx, "git", args...)
				cmd.Stdout = stdout
				cmd.Stderr = stdout
				if err := cmd.Run(); err != nil {
					return fmt.Errorf("下载%s: %w", s.Name, err)
				}
			}
		}
		if err := verifySource(ctx, path, s); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "固定源码 %s %s\n", s.Name, s.Commit); err != nil {
			return err
		}
	}
	return nil
}

func runUpstream(ctx context.Context, dir, out, filter string, strict bool, stdout io.Writer) (closeResult0 error) {
	all, err := sources()
	if err != nil {
		return err
	}
	r := upstreamReport{Target: "assistant-wasi", Filter: filter, Time: time.Now().UTC(), Sources: all, Counts: map[string]int{}, Note: "仅运行清单所列的上游测试子集；含 GNU 扩展，不代表完整套件通过率或 POSIX 认证。BLOCKED 是适配缺口，FAIL 是目标实际执行失败。宿主不提供被测命令兜底。"}
	var cases []upstreamCase
	for _, s := range all {
		path := filepath.Join(dir, s.Name)
		if err := verifySource(ctx, path, s); err != nil {
			return err
		}
		loaded, err := loadUpstream(s.Name, path)
		if err != nil {
			return fmt.Errorf("载入%s: %w", s.Name, err)
		}
		if len(loaded) == 0 {
			return fmt.Errorf("%s 未载入任何测试", s.Name)
		}
		for i := range loaded {
			loaded[i].ID = s.Name + "/" + loaded[i].ID
			loaded[i].Source = strings.TrimSuffix(s.URL, ".git") + "/blob/" + s.Commit + "/" + loaded[i].Source
		}
		cases = append(cases, loaded...)
	}
	if filter != "" {
		cases = slices.DeleteFunc(cases, func(c upstreamCase) bool { return !strings.HasPrefix(c.ID, filter) })
	}
	if len(cases) == 0 {
		return fmt.Errorf("筛选条件%s未匹配任何用例", filter)
	}
	code, err := os.ReadFile(filepath.Join("..", "..", "assistant-bash.wasm.gz"))
	if err != nil {
		return err
	}
	runtime, err := sandbox.New(ctx, sandbox.Config{ShellArchive: code, Timeout: 2 * time.Second})
	if err != nil {
		return err
	}
	defer func() {
		closeResult0 = errors.Join(closeResult0, runtime.Close())
	}()

	work, err := os.MkdirTemp("", "assistant-upstream-*")
	if err != nil {
		return err
	}
	defer func() {
		closeResult0 = errors.Join(closeResult0, os.RemoveAll(work))
	}()

	for i, c := range cases {
		if err := ctx.Err(); err != nil {
			return err
		}
		rr := upstreamResult{upstreamCase: c, Status: "BLOCKED"}
		if c.Blocked == "" {
			path := filepath.Join(work, strconv.Itoa(i))
			if err := os.Mkdir(path, 0700); err != nil {
				return err
			}
			if c.Fixture != "" {
				if err := os.CopyFS(path, os.DirFS(c.Fixture)); err != nil {
					return fmt.Errorf("准备%s: %w", c.ID, err)
				}
			}
			for name, text := range c.Files {
				if err := os.WriteFile(filepath.Join(path, name), []byte(text), 0600); err != nil {
					return err
				}
			}
			session, err := runtime.Bind(path)
			if err != nil {
				return err
			}
			var output, diagnostic bytes.Buffer
			res, runErr := session.Execute(ctx, path, c.Script, &output, &diagnostic)
			if err := session.Close(); err != nil {
				return err
			}
			rr.Executed = true
			rr.Exit = res.ExitCode
			rr.Stdout = output.String()
			rr.Diagnostic = diagnostic.String()
			rr.Status = "PASS"
			actual, expected := rr.Stdout, c.Expected
			if c.Sort {
				actual = sortOutput(actual)
				expected = sortOutput(expected)
			}
			if runErr != nil {
				rr.Error = runErr.Error()
			}
			if runErr != nil || rr.Exit != 0 || actual != expected || rr.Diagnostic != c.Stderr {
				rr.Status = "FAIL"
			}
		}
		r.Counts[rr.Status]++
		r.Results = append(r.Results, rr)
		exitDisplay := "未执行"
		if rr.Executed {
			exitDisplay = strconv.Itoa(rr.Exit)
		}
		if _, err := fmt.Fprintf(stdout, "%-7s %s exit=%s\n", rr.Status, c.ID, exitDisplay); err != nil {
			return err
		}
	}
	if err := writeUpstreamReport(out, r); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "上游子集: %v；报告 %s/report.md\n", r.Counts, out); err != nil {
		return err
	}
	if strict && (r.Counts["FAIL"] > 0 || r.Counts["BLOCKED"] > 0) {
		return fmt.Errorf("上游子集存在失败或适配缺口")
	}
	return nil
}
func sortOutput(s string) string {
	rows := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	slices.Sort(rows)
	return strings.Join(rows, "\n")
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func loadUpstream(name, dir string) ([]upstreamCase, error) {
	switch name {
	case "coreutils":
		return loadCoreutils(dir)
	case "goawk":
		return loadGolden(dir, "testdata/gawk", ".awk", ".ok", "awk")
	case "posixutils":
		return loadGolden(dir, "sh/tests/sh", ".sh", ".out", "")
	case "sed":
		return loadSed(dir)
	default:
		return nil, fmt.Errorf("未知上游%s", name)
	}
}

func loadGolden(dir, relative, extension, expectedExtension, command string) (cases []upstreamCase, resultErr error) {
	fixtureRoot, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, fixtureRoot.Close())
	}()
	root := filepath.Join(dir, relative)
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, extension) {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(rel, extension)
		c := upstreamCase{ID: strings.TrimPrefix(strings.TrimSuffix(rel, extension), relative+string(filepath.Separator)), Source: rel}
		script, err := fixtureRoot.ReadFile(rel)
		if err != nil {
			return err
		}
		expected, err := fixtureRoot.ReadFile(name + expectedExtension)
		if os.IsNotExist(err) {
			c.Blocked = "缺少 golden 输出"
		} else if err != nil {
			return err
		}
		c.Expected = string(expected)
		if command == "" {
			c.Script = string(script)

			integration, err := fixtureRoot.ReadFile("sh/tests/integration.rs")
			if err != nil {
				return err
			}
			marker := `test_script(`
			fileMarker := `include_str!("` + filepath.ToSlash(strings.TrimPrefix(rel, "sh/tests/")) + `"),`
			pattern := regexp.MustCompile(regexp.QuoteMeta(marker) + `\s*` + regexp.QuoteMeta(fileMarker))
			if !pattern.Match(integration) {
				c.Blocked = "不是 test_script 成功脚本契约，需独立 checker 适配"
			}
			if strings.Contains(c.Script, "TEST_READ_DIR") || strings.Contains(c.Script, "TEST_WRITE_DIR") {
				c.Blocked = "需要上游目录与环境 fixture 适配"
			}
		} else {
			c.Files = map[string]string{"program.awk": string(script), "stdin.txt": ""}
			input, err := fixtureRoot.ReadFile(name + ".in")
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			c.Files["stdin.txt"] = string(input)
			c.Script = "awk -f program.awk < stdin.txt"
			if slices.Contains([]string{"hex2", "rscompat", "rsstart2", "strtod"}, c.ID) {
				c.Blocked = "GoAWK 原驱动排除的兼容语义用例"
			}
			c.Sort = slices.Contains([]string{"arryref2", "delargv", "delarpm2", "forref"}, c.ID)
			if strings.Contains(c.Expected, ".awk:") {
				c.Blocked = "解释器专属错误文本，不属于 stdout golden 契约"
			}
		}
		cases = append(cases, c)
		return nil
	})
	return cases, err
}

func loadCoreutils(dir string) ([]upstreamCase, error) {
	var cases []upstreamCase
	adapter, err := filepath.Abs("adapters")
	if err != nil {
		return nil, err
	}
	for _, command := range []string{"basename", "dirname"} {
		relative := "tests/misc/" + command + ".pl"
		raw, err := exec.Command("perl", "-I", adapter, "-MAssistantVectors", filepath.Join(dir, relative)).Output()
		if err != nil {
			return nil, err
		}
		var vectors struct {
			Command string             `json:"command"`
			Tests   [][]jsontext.Value `json:"tests"`
		}
		if err := json.Unmarshal(raw, &vectors, json.RejectUnknownMembers(true)); err != nil {
			return nil, err
		}
		for _, v := range vectors.Tests {
			var id string
			if len(v) == 0 {
				return nil, fmt.Errorf("空 GNU vector")
			}
			if err := json.Unmarshal(v[0], &id); err != nil {
				return nil, err
			}
			c := upstreamCase{ID: command + "/" + id, Script: command, Source: relative}
			for _, field := range v[1:] {
				var fragment string
				if err := json.Unmarshal(field, &fragment); err == nil {
					c.Script += " " + fragment
					continue
				}
				var attributes map[string]string
				if err := json.Unmarshal(field, &attributes); err != nil {
					return nil, err
				}
				for key, value := range attributes {
					switch key {
					case "OUT":
						c.Expected = value
					case "ERR":
						c.Stderr = value
					case "ENV":
						c.Script = value + " " + c.Script
					case "EXIT":
						if value != "0" {
							c.Blocked = "GNU 诊断/非零退出契约尚未适配"
						}
					default:
						c.Blocked = "未适配 GNU vector 字段 " + key
					}
				}
			}
			cases = append(cases, c)
		}
	}
	return cases, nil
}

func loadSed(dir string) ([]upstreamCase, error) {
	relative := "tests/by-util/test_sed.rs"
	raw, err := os.ReadFile(filepath.Join(dir, relative))
	if err != nil {
		return nil, err
	}
	matches := regexp.MustCompile(`(?s)check_output!\(\s*(\w+)\s*,\s*\[(.*?)\]\s*\);`).FindAllSubmatch(raw, -1)
	var cases []upstreamCase
	for _, m := range matches {
		c := upstreamCase{ID: string(m[1]), Source: relative, Fixture: filepath.Join(dir, "tests/fixtures/sed")}
		args, err := rustArguments(string(m[2]))
		if err != nil {
			c.Blocked = err.Error()
		}
		expected, err := os.ReadFile(filepath.Join(c.Fixture, "output", c.ID))
		if err != nil {
			return nil, err
		}
		c.Expected = string(expected)
		c.Script = "sed"
		for _, arg := range args {
			c.Script += " " + shellQuote(arg)
		}
		cases = append(cases, c)
	}
	return cases, nil
}
func rustArguments(text string) ([]string, error) {
	var args []string
	constants := map[string]string{"LINES1": "input/lines1", "LINES2": "input/lines2", "NO_NEW_LINE": "input/no-new-line.txt"}
	for text = strings.TrimSpace(text); text != ""; text = strings.TrimSpace(text) {
		var value string
		consumed := 0
		if text[0] == '"' {
			for i := 1; i < len(text); i++ {
				if text[i] == '\\' {
					i++
					continue
				}
				if text[i] == '"' {
					consumed = i + 1
					break
				}
			}
			if consumed == 0 {
				return nil, fmt.Errorf("未闭合 Rust 字符串")
			}
			var err error
			value, err = strconv.Unquote(text[:consumed])
			if err != nil {
				return nil, fmt.Errorf("Rust 字符串转义需要适配: %w", err)
			}
		} else if strings.HasPrefix(text, "r") && strings.Contains(text, "\"") {
			start := strings.IndexByte(text, '"')
			prefix := text[1:start]
			if strings.Trim(prefix, "#") != "" {
				return nil, fmt.Errorf("非字面量 Rust 参数")
			}
			end := strings.Index(text[start+1:], "\""+prefix)
			if end < 0 {
				return nil, fmt.Errorf("未闭合 Rust raw 字符串")
			}
			value = text[start+1 : start+1+end]
			consumed = start + 1 + end + 1 + len(prefix)
		} else {
			end := strings.IndexByte(text, ',')
			if end < 0 {
				end = len(text)
			}
			name := strings.TrimSpace(text[:end])
			var ok bool
			value, ok = constants[name]
			if !ok {
				return nil, fmt.Errorf("未适配 Rust 参数表达式%s", name)
			}
			consumed = end
		}
		args = append(args, value)
		text = strings.TrimSpace(text[consumed:])
		if text == "" {
			break
		}
		if text[0] != ',' {
			return nil, fmt.Errorf("Rust 参数分隔无效")
		}
		text = text[1:]
	}
	return args, nil
}
func writeUpstreamReport(dir string, r upstreamReport) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	for i := range r.Results {
		c := &r.Results[i]
		c.RawBytes = map[string][]byte{}
		fields := map[string]*string{"script": &c.Script, "expected": &c.Expected, "stderr": &c.Stderr, "stdout": &c.Stdout, "diagnostic": &c.Diagnostic, "error": &c.Error}
		for name, value := range fields {
			if !utf8.ValidString(*value) {
				c.RawBytes[name] = []byte(*value)
				*value = strings.ToValidUTF8(*value, "�")
			}
		}
		files := map[string]string{}
		for name, value := range c.Files {
			if !utf8.ValidString(value) {
				c.RawBytes["file:"+name] = []byte(value)
				value = strings.ToValidUTF8(value, "�")
			}
			files[name] = value
		}
		c.Files = files
	}
	raw, err := json.Marshal(r, json.Deterministic(true))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), raw, 0600); err != nil {
		return err
	}
	var md strings.Builder
	fmt.Fprintf(&md, "# 上游兼容测试子集\n\n目标 %s · 时间 %s\n\n%s\n\n结果 %v\n\n", r.Target, r.Time.Format(time.RFC3339), r.Note+" 筛选前缀："+r.Filter, r.Counts)
	fmt.Fprintln(&md, "| 上游子集 | PASS | FAIL | BLOCKED |\n|---|---:|---:|---:|")
	for _, source := range r.Sources {
		counts := map[string]int{}
		for _, c := range r.Results {
			if strings.HasPrefix(c.ID, source.Name+"/") {
				counts[c.Status]++
			}
		}
		fmt.Fprintf(&md, "| %s | %d | %d | %d |\n", source.Name, counts["PASS"], counts["FAIL"], counts["BLOCKED"])
	}
	fmt.Fprintln(&md)
	for _, s := range r.Sources {
		fmt.Fprintf(&md, "- %s @ %s：%s\n", s.Name, s.Commit, s.Scope)
	}
	for _, c := range r.Results {
		if !c.Executed && c.Status == "BLOCKED" {
			fmt.Fprintf(&md, "\n## %s · BLOCKED\n\n来源 %s\n\n未执行：%s\n\n", c.ID, c.Source, c.Blocked)
			continue
		}
		fmt.Fprintf(&md, "\n## %s · %s\n\n来源 %s\n\n适配缺口 %s\n\n脚本\n\n```sh\n%s\n```\n\n退出 %d；运行错误 %q\n\n实际 stdout %q\n\n期望 stdout %q\n\n实际 stderr %q\n\n期望 stderr %q\n", c.ID, c.Status, c.Source, c.Blocked, c.Script, c.Exit, c.Error, c.Stdout, c.Expected, c.Diagnostic, c.Stderr)
		if len(c.RawBytes) > 0 {
			fmt.Fprintln(&md, "\n存在非 UTF-8 数据：原始字节保存在 report.json 的 raw_bytes（base64），文本仅供预览。")
		}
	}
	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(md.String()), 0600)
}
