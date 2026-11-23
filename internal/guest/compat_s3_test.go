package guest_test

import (
	"encoding/json"
	"testing"
	"time"

	nativeshell "github.com/Cosmic-Developers-Union/assistant-sandbox/guest/native"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
)

// TestCompatibilityS3DevelopmentWorkflow 验证 S3 开发工作流：真实
// Bash、BusyBox、CPython、Node.js 与客体 C 工具链在同一 VM 中完成
// 文件处理、子进程、脚本执行、编译和运行。
func TestCompatibilityS3DevelopmentWorkflow(t *testing.T) {
	machine, connection := python312GuestWith(t, func(options *instance.Options) {
		options.Session = resources.Limits{Memory: 4 << 30, Work: 1 << 38}
		options.Process = resources.Limits{Memory: 2 << 30, Work: 1 << 38}
	})
	if err := nativeshell.Install(t.Context(), machine.Filesystem()); err != nil {
		t.Fatal(err)
	}
	disablePythonVFork(t, machine)
	extractNodeRuntime(t, machine, python312Asset(t,
		"testdata/nodejs/node22-runtime.tar.gz", node22RuntimeDigest))
	extractGuestTar(t, machine, python312Asset(t,
		"testdata/tcc-riscv64-toolchain.tar.gz", tccToolchainDigest))

	env := map[string]string{
		"HOME":          "/work",
		"PATH":          "/usr/local/bin:/usr/bin:/bin",
		"LANG":          "C.UTF-8",
		"PYTHONHOME":    "/usr",
		"NODE_ICU_DATA": "/usr/share/icu/76.1",
	}
	bashScript := `set -eu
mkdir -p /work/dev/src /work/dev/out
printf 'beta\nalpha\ngamma\n' > /work/dev/src/input.txt
cat /work/dev/src/input.txt | sort > /work/dev/out/sorted.txt
grep -q alpha /work/dev/out/sorted.txt
printf 'bash-ok\n'`
	output, diagnostic, code := runGuestProgram(t, connection,
		"/bin/bash", []string{"--noprofile", "--norc", "-c", bashScript}, env, 120*time.Second)
	if code != 0 || output != "bash-ok\n" {
		t.Fatalf("S3 bash exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}

	writeGuestFile(t, machine, "/work/dev/tool.py", []byte(`from pathlib import Path
import json
import subprocess

words = Path("/work/dev/src/input.txt").read_text().splitlines()
Path("/work/dev/out/python.json").write_text(json.dumps({"count": len(words), "upper": words[0].upper()}))
subprocess.run(["/bin/sh", "-c", "printf py-sub > /work/dev/out/python-sub.txt"], check=True)
print("python-ok")
`), 0o644)
	output, diagnostic, code = runGuestProgram(t, connection,
		"/usr/bin/python3.12", []string{"/work/dev/tool.py"}, env, 300*time.Second)
	if code != 0 || output != "python-ok\n" {
		t.Fatalf("S3 python exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}

	writeGuestFile(t, machine, "/work/dev/tool.mjs", []byte(`import fs from "node:fs";
import { execFileSync } from "node:child_process";

const words = fs.readFileSync("/work/dev/src/input.txt", "utf8").trim().split("\n");
fs.writeFileSync("/work/dev/out/node.json", JSON.stringify({ count: words.length, upper: words[0].toUpperCase() }));
execFileSync("/bin/sh", ["-c", "printf node-sub > /work/dev/out/node-sub.txt"]);
console.log("node-ok");
`), 0o644)
	output, diagnostic, code = runGuestProgram(t, connection,
		"/usr/bin/node", []string{"--jitless", "/work/dev/tool.mjs"}, env, 300*time.Second)
	if code != 0 || output != "node-ok\n" {
		t.Fatalf("S3 node exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}

	writeGuestFile(t, machine, "/work/dev/tool.c", []byte(`#include <stdio.h>
#include <stddef.h>

void *memmove(void *destination, const void *source, size_t count) {
	unsigned char *to = destination;
	const unsigned char *from = source;
	if (to < from) {
		for (size_t i = 0; i < count; i++) to[i] = from[i];
	} else {
		for (size_t i = count; i > 0; i--) to[i - 1] = from[i - 1];
	}
	return destination;
}

int main(void) {
	FILE *file = fopen("/work/dev/out/c.txt", "w");
	if (!file) return 2;
	fprintf(file, "c-sub\n");
	fclose(file);
	puts("c-ok");
	return 0;
}
`), 0o644)
	output, diagnostic, code = runGuestProgram(t, connection,
		"/usr/bin/tcc", []string{"-static", "-O2", "-o", "/work/dev/tool-c", "/work/dev/tool.c"},
		env, 300*time.Second)
	if code != 0 {
		t.Fatalf("S3 tcc exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}
	output, diagnostic, code = runGuestProgram(t, connection,
		"/work/dev/tool-c", nil, env, 120*time.Second)
	if code != 0 || output != "c-ok\n" {
		t.Fatalf("S3 C program exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}

	for name, want := range map[string]string{
		"/work/dev/out/sorted.txt":     "alpha\nbeta\ngamma\n",
		"/work/dev/out/python-sub.txt": "py-sub",
		"/work/dev/out/node-sub.txt":   "node-sub",
		"/work/dev/out/c.txt":          "c-sub\n",
	} {
		got, err := readGuestFile(machine, name)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s=%q want %q", name, got, want)
		}
	}
	pythonJSON, err := readGuestFile(machine, "/work/dev/out/python.json")
	if err != nil {
		t.Fatal(err)
	}
	nodeJSON, err := readGuestFile(machine, "/work/dev/out/node.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, encoded := range map[string]string{"python": pythonJSON, "node": nodeJSON} {
		var result struct {
			Count int    `json:"count"`
			Upper string `json:"upper"`
		}
		if err := json.Unmarshal([]byte(encoded), &result); err != nil {
			t.Fatalf("%s JSON=%q: %v", name, encoded, err)
		}
		if result.Count != 3 || result.Upper != "BETA" {
			t.Fatalf("%s 结果=%+v", name, result)
		}
	}
}
