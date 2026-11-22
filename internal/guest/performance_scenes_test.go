package guest_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
)

// TestPythonNodeSceneBaseline 收集 T09 要求的客体场景基线:启动、文件
// 处理与并发服务耗时,并读取 VM 会话预算中的内存数据。该测试只记录
// 可复测基线,不用宿主缓存或解释器替代客体执行。
func TestPythonNodeSceneBaseline(t *testing.T) {
	t.Run("python", func(t *testing.T) {
		started := time.Now()
		machine, connection := python312Guest(t)
		startup := time.Since(started)
		script := `import os, resource, threading
payload = b"x" * (1 << 20)
with open("/work/t09.bin", "wb") as output:
    output.write(payload)
assert open("/work/t09.bin", "rb").read() == payload
count = [0]
lock = threading.Lock()
def worker():
    for _ in range(2000):
        with lock: count[0] += 1
threads = [threading.Thread(target=worker) for _ in range(4)]
for thread in threads: thread.start()
for thread in threads: thread.join()
assert count[0] == 8000
print("T09-PYTHON maxrss_kib=%d" % resource.getrusage(resource.RUSAGE_SELF).ru_maxrss)
`
		workStarted := time.Now()
		output, code := runPython312(t, connection, script, 600*time.Second)
		work := time.Since(workStarted)
		if code != 0 || !containsText(output, "T09-PYTHON") {
			t.Fatalf("Python baseline exit=%d output=%q", code, output)
		}
		status, err := machine.Status(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("T09 python startup=%v work=%v session_memory=%d processes=%d output=%q",
			startup, work, status.Session.MemoryUsed, len(status.Processes), output)
	})

	t.Run("node", func(t *testing.T) {
		started := time.Now()
		machine, connection := node22RealtimeGuest(t)
		startup := time.Since(started)
		script := `const fs = require("node:fs");
const data = Buffer.alloc(1 << 20, 0x78);
fs.writeFileSync("/work/t09-node.bin", data);
if (!fs.readFileSync("/work/t09-node.bin").equals(data)) throw new Error("file");
let count = 0;
for (let i = 0; i < 20000; i++) count = (count + 1) | 0;
if (count !== 20000) throw new Error("work");
process.stdout.write("T09-NODE rss=" + process.memoryUsage().rss + "\n");
`
		workStarted := time.Now()
		output, diagnostic, code := runNode22(t, connection, []string{"--jitless", "-e", script}, 600*time.Second)
		work := time.Since(workStarted)
		if code != 0 || !containsText(output, "T09-NODE") {
			t.Fatalf("Node baseline exit=%d stdout=%q stderr=%q", code, output, diagnostic)
		}
		status, err := machine.Status(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("T09 node startup=%v work=%v session_memory=%d processes=%d output=%q",
			startup, work, status.Session.MemoryUsed, len(status.Processes), output)
	})
}

func containsText(value, part string) bool {
	for len(value) >= len(part) {
		if value[:len(part)] == part {
			return true
		}
		value = value[1:]
	}
	return false
}

func TestRuntimeMemorySnapshot(t *testing.T) {
	python, pythonConnection := python312Guest(t)
	runMemoryProbe(t, python, pythonConnection, "/usr/bin/python3.12",
		[]string{"-S", "-c", "import time; data=bytearray(8*1024*1024); print('READY', flush=True); time.sleep(3)"})
	node, nodeConnection := node22RealtimeGuest(t)
	runMemoryProbe(t, node, nodeConnection, "/usr/bin/node",
		[]string{"--jitless", "-e", "const data=Buffer.alloc(8*1024*1024); console.log('READY'); setTimeout(()=>{},3000)"})
}

func runMemoryProbe(t *testing.T, machine *instance.VM, connection *instance.Connection, program string, args []string) {
	t.Helper()
	if connection == nil || machine == nil {
		t.Fatal("missing VM")
	}
	output := &bytes.Buffer{}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	process, err := connection.Exec(ctx, instance.ExecSpec{
		Program: program,
		Args:    args,
		Dir:     "/work",
		Env:     map[string]string{"PATH": "/usr/bin:/bin", "HOME": "/work"},
		Streams: fd.Streams{Output: output},
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	status, err := machine.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range status.Processes {
		if candidate.ID == uint64(process.ID) {
			t.Logf("memory snapshot pid=%d memory_used=%d memory_limit=%d descriptor_used=%d",
				candidate.ID, candidate.Budget.MemoryUsed, candidate.Budget.MemoryLimit, candidate.Budget.DescriptorUsed)
		}
	}
	if _, err := connection.Wait(ctx, process.ID); err != nil {
		t.Fatalf("memory probe: %v output=%q", err, output.String())
	}
}
