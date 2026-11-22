package guest_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
)

// TestPythonNodeCoexist 验证 Q31:同一 VM 中 Python 与 Node 通过文件、
// 管道和回环 HTTP 交换数据,断开重连不影响后台 Node 服务。
func TestPythonNodeCoexist(t *testing.T) {
	machine, connection := node22GuestWith(t, func(options *instance.Options) {
		options.Deterministic = false
	})
	apk := python312Asset(t, "testdata/python312/python3-3.12.16-r0.apk", python312APKDigest)
	pyc := python312Asset(t, "testdata/python312/python312-pyc.tar.gz", python312PycDigest)
	libs := python312Asset(t, "testdata/python312/python312-libs.tar.gz", python312LibsDigest)
	loader, err := os.ReadFile("testdata/lib/ld-musl-riscv64.so.1")
	if err != nil {
		t.Fatal(err)
	}
	extractPython312(t, machine, apk)
	extractPython312Pyc(t, machine, pyc)
	extractPython312Libs(t, machine, libs)
	writeGuestFile(t, machine, "/lib/ld-musl-riscv64.so.1", loader, 0o755)
	writeGuestFile(t, machine, "/lib/libc.musl-riscv64.so.1", loader, 0o755)

	nodeServer := `const fs = require("node:fs");
const http = require("node:http");
const { execFile } = require("node:child_process");
const server = http.createServer((request, response) => {
  if (request.url === "/ping") {
    response.end("node:" + request.headers["x-q31"]);
    return;
  }
  if (request.url === "/write") {
    fs.writeFileSync("/work/q31_node.txt", "from-node");
    response.end("written");
    return;
  }
  if (request.url === "/read-python") {
    response.end(fs.readFileSync("/work/q31_python.txt", "utf8"));
    return;
  }
  if (request.url === "/pipe") {
    const child = execFile("/usr/bin/python3.12", ["-S", "-c", "import sys; print('python:' + sys.stdin.read(), end='')"], {
      env: { PATH: "/usr/bin:/bin", HOME: "/work", LANG: "C.UTF-8", PYTHONHOME: "/usr" },
    }, (error, stdout, stderr) => {
      if (error) { response.end("error:" + (error.code || error.message)); return; }
      fs.writeFileSync("/work/q31_pipe.txt", stdout);
      response.end(stdout);
    });
    child.stdin.end("pipe");
    return;
  }
  response.end("unknown");
});
server.listen(39201, "127.0.0.1", () => {
  fs.writeFileSync("/work/q31_node.ready", "ready");
});
setInterval(() => {}, 1000);
`
	writeGuestFile(t, machine, "/work/q31_node.js", []byte(nodeServer), 0o644)
	nodeProcess, err := connection.Exec(t.Context(), instance.ExecSpec{
		Program: "/usr/bin/node",
		Args:    []string{"--jitless", "/work/q31_node.js"},
		Dir:     "/work",
		Env:     map[string]string{"PATH": "/usr/bin:/bin", "HOME": "/work", "LANG": "C.UTF-8", "NODE_ICU_DATA": "/usr/share/icu/76.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(120 * time.Second)
	for {
		if _, err := readGuestFile(machine, "/work/q31_node.ready"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Node 后台服务未就绪")
		}
		time.Sleep(500 * time.Millisecond)
	}

	pythonClient := `import os, sys, time, urllib.request

def mark(value):
    with open("/work/q31.status", "w") as output:
        output.write(value + "\n")
        output.flush()
mark("client-start")

deadline = time.time() + 120
while not os.path.exists("/work/q31_node.ready"):
    assert time.time() < deadline, "node not ready"
    time.sleep(0.5)
mark("node-ready")
request = urllib.request.Request("http://127.0.0.1:39201/ping", headers={"X-Q31": "python"})
reply = urllib.request.urlopen(request, timeout=30).read().decode()
assert reply == "node:python", reply
print("PING-OK", flush=True)
mark("ping")
urllib.request.urlopen("http://127.0.0.1:39201/write", timeout=30).read()
assert open("/work/q31_node.txt").read() == "from-node"
print("FILE-NODE-OK", flush=True)
mark("node-file")
open("/work/q31_python.txt", "w").write("from-python")
assert urllib.request.urlopen("http://127.0.0.1:39201/read-python", timeout=30).read().decode() == "from-python"
print("FILE-PYTHON-OK", flush=True)
mark("python-file")
pipe = urllib.request.urlopen("http://127.0.0.1:39201/pipe", timeout=120).read().decode()
assert pipe == "python:pipe", pipe
assert open("/work/q31_pipe.txt").read() == "python:pipe"
print("PIPE-OK", flush=True)
print("PYTHON-HTTP-OK")
`
	output, code, runErr := runPython312Outcome(t, connection, pythonClient, 600*time.Second, nil)
	if runErr != nil || code != 0 || !strings.Contains(output, "PYTHON-HTTP-OK") {
		status, _ := readGuestFile(machine, "/work/q31.status")
		t.Fatalf("Python/Node HTTP exit=%d status=%q output=%q err=%v", code, status, output, runErr)
	}
	if err := connection.Disconnect(t.Context()); err != nil {
		t.Fatal(err)
	}
	reconnected, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reconnectClient := `import urllib.request
request = urllib.request.Request("http://127.0.0.1:39201/ping", headers={"X-Q31": "reconnect"})
reply = urllib.request.urlopen(request, timeout=30).read().decode()
assert reply == "node:reconnect", reply
assert open("/work/q31_pipe.txt").read() == "python:pipe"
print("RECONNECT-OK")
`
	output, code, runErr = runPython312Outcome(t, reconnected, reconnectClient, 300*time.Second, nil)
	if runErr != nil || code != 0 || !strings.Contains(output, "RECONNECT-OK") {
		t.Fatalf("重连后 Node 服务 exit=%d output=%q err=%v", code, output, runErr)
	}
	_ = nodeProcess
}
