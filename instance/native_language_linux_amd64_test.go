//go:build linux && amd64

package instance

import (
	"context"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeGuestLanguagesAndNativeExtensions(t *testing.T) {
	// Neither the host test process nor the guest commands may rely on a
	// node/python executable discovered through PATH.
	hostPath := t.TempDir()
	t.Setenv("PATH", hostPath)

	machine, err := newNativeVMForTest(t, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = machine.Close(t.Context()) }()

	work := filepath.Join(machine.Root(), "work")
	stageNativeLanguageFile(t, "../internal/guest/testdata/nodejs/q23/q23_addon_x86_64.node", filepath.Join(work, "q23_addon_x86_64.node"), 0o755)
	// The fixed fixture is named q06_bad, but its initializer is
	// PyInit_q06_native. A generic .so suffix also avoids coupling the test
	// to the hostroot's Python ABI suffix.
	stageNativeLanguageFile(t, "../internal/guest/testdata/python312/q06_bad.cpython-312-x86_64-linux-gnu.so", filepath.Join(work, "q06_native.so"), 0o755)
	stageNativeLanguageScript(t, work, "language-server.js", nativeLanguageServerScript)
	stageNativeLanguageScript(t, work, "language-node.js", nativeLanguageNodeScript)
	stageNativeLanguageScript(t, work, "language-python.py", nativeLanguagePythonScript)

	output, err := os.CreateTemp(t.TempDir(), "native-language-output")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = output.Close() }()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	state, err := machine.Run(ctx, NativeExecSpec{
		Program: "/bin/sh",
		Args:    []string{"-c", nativeLanguageShellScript, "sh"},
		Dir:     "/work",
		Env:     map[string]string{"PATH": "/nonexistent"},
		Stdout:  output,
		Stderr:  output,
	})
	data, readErr := os.ReadFile(output.Name())
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err != nil || state.Code != 0 || state.Signal != 0 {
		t.Fatalf("guest languages exit=%+v err=%v output=%s", state, err, data)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("guest language output=%q, want two JSON results", data)
	}
	var nodeResult nativeLanguageNodeResult
	if err := json.Unmarshal([]byte(lines[0]), &nodeResult); err != nil {
		t.Fatalf("decode Node result: %v output=%s", err, data)
	}
	var pythonResult nativeLanguagePythonResult
	if err := json.Unmarshal([]byte(lines[1]), &pythonResult); err != nil {
		t.Fatalf("decode Python result: %v output=%s", err, data)
	}

	if nodeResult.ExecPath != "/usr/bin/node" || nodeResult.Arch != "x64" || nodeResult.Platform != "linux" {
		t.Fatalf("unexpected Node interpreter: %+v", nodeResult)
	}
	if nodeResult.AddonType != "function" || nodeResult.AddonSum != 5 {
		t.Fatalf("unexpected N-API addon result: %+v", nodeResult)
	}
	if nodeResult.Worker.ExecPath != "/usr/bin/node" || nodeResult.Worker.Arch != "x64" || nodeResult.Worker.Sum != 42 {
		t.Fatalf("unexpected Node worker result: %+v", nodeResult.Worker)
	}
	if nodeResult.Work != "node-write" {
		t.Fatalf("Node /work round trip=%q", nodeResult.Work)
	}
	if nodeResult.HTTP.Message != "node-loopback-ok" || nodeResult.HTTP.PID <= 0 {
		t.Fatalf("unexpected Node loopback response: %+v", nodeResult.HTTP)
	}

	if pythonResult.ExecPath != "/usr/bin/python3.12" || pythonResult.Machine != "x86_64" || pythonResult.PointerBits != 64 {
		t.Fatalf("unexpected Python interpreter: %+v", pythonResult)
	}
	if pythonResult.AddonSum != 5 {
		t.Fatalf("unexpected CPython extension result: %+v", pythonResult)
	}
	if pythonResult.NodeWork != "node-write" || pythonResult.Work != "python-write" {
		t.Fatalf("unexpected Python /work round trip: %+v", pythonResult)
	}
	if pythonResult.Child.ExecPath != "/usr/bin/python3.12" || pythonResult.Child.Machine != "x86_64" || pythonResult.Child.PointerBits != 64 ||
		pythonResult.Child.NodeWork != "node-write" || pythonResult.Child.Work != "python-write" {
		t.Fatalf("unexpected Python subprocess result: %+v", pythonResult.Child)
	}
	if pythonResult.HTTP.Message != "python-loopback-ok" || pythonResult.HTTP.PID != nodeResult.HTTP.PID {
		t.Fatalf("Python loopback response=%+v, Node server PID=%d", pythonResult.HTTP, nodeResult.HTTP.PID)
	}

	for name, want := range map[string]string{
		"node-language.txt":   "node-write",
		"python-language.txt": "python-write",
	} {
		data, err := os.ReadFile(filepath.Join(work, name))
		if err != nil || string(data) != want {
			t.Fatalf("host read /work/%s=%q err=%v, want %q", name, data, err, want)
		}
	}
	t.Logf("host PATH=%s; Node and Python used absolute guest interpreter paths", hostPath)
}

func stageNativeLanguageFile(t *testing.T, source, destination string, mode os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, data, mode); err != nil {
		t.Fatal(err)
	}
}

func stageNativeLanguageScript(t *testing.T, work, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(work, name), []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
}

type nativeLanguageHTTPResult struct {
	Message string `json:"message"`
	PID     int    `json:"pid"`
}

type nativeLanguageNodeResult struct {
	ExecPath  string `json:"execPath"`
	Arch      string `json:"arch"`
	Platform  string `json:"platform"`
	AddonSum  int    `json:"addonSum"`
	AddonType string `json:"addonType"`
	Worker    struct {
		ExecPath string `json:"execPath"`
		Arch     string `json:"arch"`
		Sum      int    `json:"sum"`
	} `json:"worker"`
	Work string                   `json:"work"`
	HTTP nativeLanguageHTTPResult `json:"http"`
}

type nativeLanguagePythonResult struct {
	ExecPath    string `json:"execPath"`
	Machine     string `json:"machine"`
	PointerBits int    `json:"pointerBits"`
	AddonSum    int    `json:"addonSum"`
	NodeWork    string `json:"nodeWork"`
	Work        string `json:"work"`
	Child       struct {
		ExecPath    string `json:"execPath"`
		Machine     string `json:"machine"`
		PointerBits int    `json:"pointerBits"`
		NodeWork    string `json:"nodeWork"`
		Work        string `json:"work"`
	} `json:"child"`
	HTTP nativeLanguageHTTPResult `json:"http"`
}

const nativeLanguageShellScript = `set -eu
/usr/bin/node /work/language-server.js &
server=$!
trap 'kill "$server" 2>/dev/null || true' 0 1 2 15
attempt=0
while [ ! -s /work/language-server.port ]; do
  attempt=$((attempt + 1))
  [ "$attempt" -lt 100000 ] || exit 1
done
/usr/bin/node /work/language-node.js
/usr/bin/python3.12 /work/language-python.py
wait "$server"
`

const nativeLanguageServerScript = `const fs = require("node:fs");
const http = require("node:http");
let requests = 0;
const server = http.createServer((request, response) => {
  if (request.url !== "/node" && request.url !== "/python") {
    response.writeHead(404);
    response.end("not-found");
    return;
  }
  requests += 1;
  response.writeHead(200, { "content-type": "application/json", connection: "close" });
  response.end(JSON.stringify({
    message: request.url === "/node" ? "node-loopback-ok" : "python-loopback-ok",
    pid: process.pid
  }));
  if (requests === 2) server.close();
});
server.listen(0, "127.0.0.1", () => {
  fs.writeFileSync("/work/language-server.port", String(server.address().port));
});
`

const nativeLanguageNodeScript = `const fs = require("node:fs");
const { Worker } = require("node:worker_threads");
const addon = require("/work/q23_addon_x86_64.node");

(async () => {
  fs.writeFileSync("/work/node-language.txt", "node-write");
  const work = fs.readFileSync("/work/node-language.txt", "utf8");
  const worker = new Worker(` + "`" + `
    const { parentPort } = require("node:worker_threads");
    parentPort.postMessage({
      execPath: process.execPath,
      arch: process.arch,
      sum: 20 + 22
    });
  ` + "`" + `, { eval: true });
  const workerResult = await new Promise((resolve, reject) => {
    worker.once("message", resolve);
    worker.once("error", reject);
  });
  const port = fs.readFileSync("/work/language-server.port", "utf8").trim();
  const response = await fetch("http://127.0.0.1:" + port + "/node");
  if (!response.ok) throw new Error("loopback status " + response.status);
  const http = await response.json();
  console.log(JSON.stringify({
    execPath: process.execPath,
    arch: process.arch,
    platform: process.platform,
    addonSum: addon.add(2, 3),
    addonType: typeof addon.add,
    worker: workerResult,
    work,
    http
  }));
})().catch((error) => {
  console.error(error && error.stack || error);
  process.exitCode = 1;
});
`

const nativeLanguagePythonScript = `import json
import platform
import struct
import subprocess
import sys
import urllib.request
from pathlib import Path

import q06_native

node_work = Path("/work/node-language.txt").read_text()
work_path = Path("/work/python-language.txt")
work_path.write_text("python-write")
work = work_path.read_text()
child_code = """
import json
import platform
import struct
import sys
from pathlib import Path
print(json.dumps({
    "execPath": sys.executable,
    "machine": platform.machine(),
    "pointerBits": struct.calcsize("P") * 8,
    "nodeWork": Path("/work/node-language.txt").read_text(),
    "work": Path("/work/python-language.txt").read_text()
}))
"""
child = subprocess.run(
    [sys.executable, "-c", child_code],
    check=True,
    capture_output=True,
    text=True,
)
port = Path("/work/language-server.port").read_text().strip()
with urllib.request.urlopen("http://127.0.0.1:" + port + "/python") as response:
    http_result = json.load(response)
print(json.dumps({
    "execPath": sys.executable,
    "machine": platform.machine(),
    "pointerBits": struct.calcsize("P") * 8,
    "addonSum": q06_native.add(2, 3),
    "nodeWork": node_work,
    "work": work,
    "child": json.loads(child.stdout),
    "http": http_result
}))
`
