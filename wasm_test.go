package sandbox

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWASIBashAndWorkspace(t *testing.T) {
	r, err := newTestRuntime(t.Context(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	dir := t.TempDir()
	var out, diagnostic bytes.Buffer
	script := `set -e; values=(one two); mkdir -p sub; printf '%s\n' "${values[@]}" > sub/value; cp sub/value copy; mv copy moved; cat moved | base64 > encoded; base64 -d encoded; ls sub; find sub -name value; touch touched; shasum moved; test -f touched; rm moved; [[ ! -f moved ]]`
	result, err := r.Execute(t.Context(), dir, script, &out, &diagnostic)
	if err != nil || result.ExitCode != 0 || !strings.Contains(out.String(), "one\ntwo\n") {
		t.Fatal(result, err, out.String(), diagnostic.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "sub", "value"))
	if err != nil || string(data) != "one\ntwo\n" {
		t.Fatal(string(data), err)
	}
	for _, script := range []string{"go version", "git status", "/bin/dash -c true", "python3 --version"} {
		out.Reset()
		result, err := r.Execute(t.Context(), dir, script, &out, &out)
		if err != nil || result.ExitCode != 127 {
			t.Fatal(script, result, err)
		}
	}

	out.Reset()
	if result, err := r.Execute(t.Context(), dir, "/bin/sh -c 'go version'", &out, &out); err != nil || result.ExitCode != 127 {
		t.Fatal(result, err, out.String())
	}
	result, err = r.Execute(t.Context(), dir, "exit 7", &out, &out)
	if err != nil || result.ExitCode != 7 {
		t.Fatal(result, err)
	}
	result, err = r.Execute(t.Context(), dir, "if", &out, &out)
	if err != nil || result.ExitCode != 2 {
		t.Fatal(result, err)
	}
}
func TestWASINoHostCapabilitiesOrEscapes(t *testing.T) {
	t.Setenv("AGENT_API_KEY", "host-secret")
	dir := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("outside-secret"), 0o600); err != nil {
		t.Error(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "link")); err != nil {
		t.Skip("该平台不可创建符号链接")
	}
	r, err := newTestRuntime(t.Context(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	for _, script := range []string{"cat link", "cat /etc/passwd", "cat ../secret", "echo leaked > link", "echo leaked > /escape", "cat /proc/self/environ", "mkdir /etc/escape"} {
		var out bytes.Buffer
		result, err := r.Execute(t.Context(), dir, script, &out, &out)
		if err != nil || result.ExitCode == 0 || strings.Contains(out.String(), "outside-secret") {
			t.Fatal(script, result, err, out.String())
		}
	}
	var out bytes.Buffer
	result, err := r.Execute(t.Context(), dir, `test -z "$AGENT_API_KEY" && echo isolated`, &out, &out)
	if err != nil || result.ExitCode != 0 || out.String() != "isolated\n" {
		t.Fatal(result, err, out.String())
	}
	data, _ := os.ReadFile(secret)
	if string(data) != "outside-secret" {
		t.Fatal("修改了根外文件")
	}
}
func TestWASIBudgetsCancellationAndCleanup(t *testing.T) {
	dir := t.TempDir()
	r, err := newTestRuntime(t.Context(), Config{Timeout: 100 * time.Millisecond, OutputBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := r.Execute(t.Context(), dir, `while true; do :; done`, io.Discard, io.Discard); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err := r.Execute(t.Context(), dir, `printf '%s' 12345678901234567890`, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "输出") {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Execute(ctx, dir, "true", io.Discard, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, script := range []string{"", strings.Repeat("x", 64<<10+1)} {
		if _, err := r.Execute(t.Context(), dir, script, io.Discard, io.Discard); err == nil {
			t.Fatal("脚本上限")
		}
	}
	if _, err := r.Execute(t.Context(), filepath.Join(dir, "missing"), "true", io.Discard, io.Discard); err == nil {
		t.Fatal("缺少工作区")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Execute(t.Context(), dir, "true", io.Discard, io.Discard); err == nil {
		t.Fatal("关闭后执行")
	}
}
func TestWASIConfigAndReadOnly(t *testing.T) {
	for _, cfg := range []Config{{MemoryMB: -1}, {MemoryMB: 4097}, {Concurrency: 129}, {Timeout: -1}, {MaxFileBytes: -1}, {WriteBytes: -1}, {OutputBytes: -1}, {Modules: map[string][]byte{"bad/name": {}}}, {Modules: map[string][]byte{"bad": {1, 2}}}} {
		if r, err := newTestRuntime(t.Context(), cfg); err == nil {
			if err := r.Close(); err != nil {
				t.Error(err)
			}
			t.Fatal(cfg)
		}
	}
	r, err := newTestRuntime(t.Context(), Config{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	dir := t.TempDir()
	result, err := r.Execute(t.Context(), dir, "echo blocked > value", io.Discard, io.Discard)
	if err != nil || result.ExitCode == 0 {
		t.Fatal(result, err)
	}
	result, err = r.Execute(t.Context(), dir, "echo allowed > /tmp/value; cat /tmp/value", io.Discard, io.Discard)
	if err != nil || result.ExitCode != 0 {
		t.Fatal(result, err)
	}
	result, err = r.Execute(t.Context(), dir, "test -f /tmp/value", io.Discard, io.Discard)
	if err != nil || result.ExitCode != 0 {
		t.Fatal("持续会话丢失临时目录", result, err)
	}
}

func TestPersistentBashLoopAndSessionIsolation(t *testing.T) {
	r, err := newTestRuntime(t.Context(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Error(err)
	}
	first, err := r.Bind(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := first.Close(); err != nil {
			t.Error(err)
		}
	}()
	second, err := r.Bind(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := second.Close(); err != nil {
			t.Error(err)
		}
	}()
	var out bytes.Buffer
	result, err := first.Execute(t.Context(), dir, `cd sub; VALUE=remembered; answer() { printf '%s' "$VALUE"; }; echo scratch > /tmp/value`, &out, &out)
	if err != nil || result.ExitCode != 0 {
		t.Fatal(result, err, out.String())
	}
	out.Reset()
	result, err = first.Execute(t.Context(), dir, `answer; pwd; cat /tmp/value`, &out, &out)
	if err != nil || result.ExitCode != 0 || !strings.Contains(out.String(), "remembered/work/sub") || !strings.Contains(out.String(), "scratch") {
		t.Fatal(result, err, out.String())
	}
	out.Reset()
	result, err = second.Execute(t.Context(), dir, `test -z "$VALUE"; test ! -f /tmp/value; pwd`, &out, &out)
	if err != nil || result.ExitCode != 0 || out.String() != "/work\n" {
		t.Fatal(result, err, out.String())
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := first.Execute(ctx, dir, "VALUE=changed", &out, &out); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	out.Reset()
	result, err = first.Execute(t.Context(), dir, `answer`, &out, &out)
	if err != nil || result.ExitCode != 0 || out.String() != "remembered" {
		t.Fatal(result, err, out.String())
	}
}

func TestEmbeddedModuleValidation(t *testing.T) {
	if _, err := newRuntime(t.Context(), Config{}, []byte("corrupt")); err == nil {
		t.Fatal("无效客体压缩包")
	}
	var archive bytes.Buffer
	writer := gzip.NewWriter(&archive)
	if _, err := writer.Write([]byte("not-wasm")); err != nil {
		t.Error(err)
	}
	if err := writer.Close(); err != nil {
		t.Error(err)
	}
	if _, err := newRuntime(t.Context(), Config{}, archive.Bytes()); err == nil {
		t.Fatal("无效 WASM 客体")
	}
	truncated := shellArchive[:len(shellArchive)-4]
	if _, err := newRuntime(t.Context(), Config{}, truncated); err == nil {
		t.Fatal("截断的客体压缩包")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := newTestRuntime(ctx, Config{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	modules := map[string][]byte{}
	for i := range 33 {
		modules[fmt.Sprint(i)] = nil
	}
	if _, err := newTestRuntime(t.Context(), Config{Modules: modules}); err == nil {
		t.Fatal("模块数量上限")
	}
	reader, _ := gzip.NewReader(bytes.NewReader(shellArchive))
	code, _ := io.ReadAll(reader)
	if err := reader.Close(); err != nil {
		t.Error(err)
	}
	if _, err := newTestRuntime(t.Context(), Config{Modules: map[string][]byte{"illegal": code}}); err == nil {
		t.Fatal("扩展导入了宿主命令能力")
	}
	memoryImport := []byte{0, 97, 115, 109, 1, 0, 0, 0, 2, 15, 1, 3, 'e', 'n', 'v', 6, 'm', 'e', 'm', 'o', 'r', 'y', 2, 0, 1}
	if _, err := newTestRuntime(t.Context(), Config{Modules: map[string][]byte{"illegal": memoryImport}}); err == nil {
		t.Fatal("扩展导入了外部内存")
	}
}

func TestWASINullDeviceAndTextPipeline(t *testing.T) {
	r, err := newTestRuntime(t.Context(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	dir := t.TempDir()
	var out, diagnostic bytes.Buffer
	script := `set -e
printf discarded > /dev/null
cat /dev/null
cat missing 2>/dev/null || true
test -c /dev/null
printf 'beta:2\nalpha:1\nalpha:1\n' | sort -u | cut -d : -f 1 | tee names | head -n 1
grep -n beta names
wc -l < names
printf 'a\nb\n' | tail -n 1
printf 'a\na\n' | uniq -c`
	result, err := r.Execute(t.Context(), dir, script, &out, &diagnostic)
	if err != nil || result.ExitCode != 0 || out.String() != "alpha\n2:beta\n2\nb\n2 a\n" || diagnostic.Len() != 0 {
		t.Fatal(result, err, out.String(), diagnostic.String())
	}
	result, err = r.Execute(t.Context(), dir, `echo bad > /dev/other`, &out, &diagnostic)
	if err != nil || result.ExitCode == 0 {
		t.Fatal("创建了非授权设备", result, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "null")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("空设备写入工作区", err)
	}
}

func elfImage(t *testing.T, guestPath string) []byte {
	t.Helper()
	const (
		codeBase   = 0x10000
		dataBase   = 0x20000
		dataOffset = 0x2000
		entry      = 0x10100
		pathAt     = 0x300
		programAt  = 0x100
		codeSize   = 0x1000
		dataSize   = 0x100
	)
	raw := make([]byte, dataOffset+dataSize)
	copy(raw[0:4], "\x7fELF")
	raw[4], raw[5], raw[6] = 2, 1, 1
	binary.LittleEndian.PutUint16(raw[16:], 2)
	binary.LittleEndian.PutUint16(raw[18:], 243)
	binary.LittleEndian.PutUint32(raw[20:], 1)
	binary.LittleEndian.PutUint64(raw[24:], entry)
	binary.LittleEndian.PutUint64(raw[32:], 0x40)
	binary.LittleEndian.PutUint16(raw[52:], 64)
	binary.LittleEndian.PutUint16(raw[54:], 56)
	binary.LittleEndian.PutUint16(raw[56:], 2)
	putPhdr(raw[0x40:], 5, 0, codeBase, codeSize)
	putPhdr(raw[0x78:], 6, dataOffset, dataBase, dataSize)
	copy(raw[pathAt:], append([]byte(guestPath), 0))
	for index, instruction := range []uint32{
		0xF9C00513,
		0x000105B7,
		0x30058593,
		0x00000613,
		0x00000693,
		0x03800893,
		0x00000073,
		0x000205B7,
		0x00200613,
		0x03F00893,
		0x00000073,
		0x00100513,
		0x000205B7,
		0x00200613,
		0x04000893,
		0x00000073,
		0x00700513,
		0x05D00893,
		0x00000073,
	} {
		binary.LittleEndian.PutUint32(raw[programAt+index*4:], instruction)
	}
	return raw
}

func putPhdr(dst []byte, flags uint32, offset, vaddr, size uint64) {
	binary.LittleEndian.PutUint32(dst[0:], 1)
	binary.LittleEndian.PutUint32(dst[4:], flags)
	binary.LittleEndian.PutUint64(dst[8:], offset)
	binary.LittleEndian.PutUint64(dst[16:], vaddr)
	binary.LittleEndian.PutUint64(dst[32:], size)
	binary.LittleEndian.PutUint64(dst[40:], size)
	binary.LittleEndian.PutUint64(dst[48:], 0x1000)
}

func TestWASIRunsELFProgram(t *testing.T) {
	r, err := newTestRuntime(t.Context(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	dir := t.TempDir()
	var out, diagnostic bytes.Buffer
	image := base64.StdEncoding.EncodeToString(elfImage(t, "/work/data"))
	data := base64.StdEncoding.EncodeToString([]byte("hi"))
	script := fmt.Sprintf("printf '%%s' '%s' | base64 -d > data; printf '%%s' '%s' | base64 -d > program; test -s program; /work/program; printf 'status=%%s' \"$?\"", data, image)
	if _, err := r.Execute(t.Context(), dir, script, &out, &diagnostic); err != nil {
		t.Fatal(err, out.String(), diagnostic.String())
	}
	if !strings.Contains(out.String(), "hi") || !strings.Contains(out.String(), "status=7") {
		t.Fatalf("输出=%q 诊断=%q", out.String(), diagnostic.String())
	}
}
