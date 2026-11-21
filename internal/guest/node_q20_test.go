package guest_test

import (
	"encoding/json"
	"testing"
	"time"
)

// TestNode22WorkerThreads 验证 Q20:worker_threads 消息通信、
// SharedArrayBuffer/Atomics 跨线程唤醒,以及 worker 异常与主线隔离。
func TestNode22WorkerThreads(t *testing.T) {
	machine, connection := node22RealtimeGuest(t)
	script := `const { Worker } = require("node:worker_threads");

(async () => {
  const result = {};

  const echo = new Worker(` + "`" + `
    const { parentPort, workerData } = require("node:worker_threads");
    parentPort.on("message", (message) => {
      parentPort.postMessage({ type: "pong", value: message.value + 1 });
    });
    parentPort.postMessage({ type: "ready", value: workerData.start });
  ` + "`" + `, { eval: true, workerData: { start: 41 } });
  result.ready = await new Promise((resolve, reject) => {
    echo.once("message", resolve);
    echo.once("error", reject);
  });
  echo.postMessage({ type: "ping", value: 1 });
  result.pong = await new Promise((resolve, reject) => {
    echo.once("message", resolve);
    echo.once("error", reject);
  });

  const shared = new SharedArrayBuffer(8);
  const view = new Int32Array(shared);
  const waiter = new Worker(` + "`" + `
    const { parentPort, workerData } = require("node:worker_threads");
    const view = new Int32Array(workerData);
    parentPort.postMessage({ type: "waiting" });
    const status = Atomics.wait(view, 0, 0, 20000);
    parentPort.postMessage({ type: "woke", status, value: Atomics.load(view, 0) });
  ` + "`" + `, { eval: true, workerData: shared });
  result.waiting = await new Promise((resolve, reject) => {
    waiter.once("message", resolve);
    waiter.once("error", reject);
  });
  Atomics.store(view, 0, 77);
  Atomics.notify(view, 0);
  result.atomics = await new Promise((resolve, reject) => {
    waiter.once("message", resolve);
    waiter.once("error", reject);
  });

  const failing = new Worker(` + "`" + `throw new Error("worker-boom");` + "`" + `, { eval: true });
  result.error = await new Promise((resolve) => {
    failing.once("error", (error) => resolve({ message: error.message }));
  });
  result.main = 7;

  await echo.terminate();
  await waiter.terminate();
  process.stdout.write(JSON.stringify(result) + "\n");
})().catch((error) => {
  process.stderr.write(String(error && error.stack || error) + "\n");
  process.exitCode = 1;
});
`
	writeGuestFile(t, machine, "/work/q20_workers.js", []byte(script), 0o644)
	output, diagnostic, code := runNode22(t, connection, []string{"--jitless", "/work/q20_workers.js"}, 600*time.Second)
	if code != 0 {
		t.Fatalf("worker_threads exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}
	var result struct {
		Ready struct {
			Type  string `json:"type"`
			Value int    `json:"value"`
		} `json:"ready"`
		Pong struct {
			Type  string `json:"type"`
			Value int    `json:"value"`
		} `json:"pong"`
		Waiting struct {
			Type string `json:"type"`
		} `json:"waiting"`
		Atomics struct {
			Type   string `json:"type"`
			Status string `json:"status"`
			Value  int    `json:"value"`
		} `json:"atomics"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Main int `json:"main"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("worker_threads 输出非法: %v (%q)", err, output)
	}
	if result.Ready.Type != "ready" || result.Ready.Value != 41 {
		t.Fatalf("worker ready=%+v", result.Ready)
	}
	if result.Pong.Type != "pong" || result.Pong.Value != 2 {
		t.Fatalf("worker pong=%+v", result.Pong)
	}
	if result.Waiting.Type != "waiting" {
		t.Fatalf("worker 未进入 Atomics.wait: %+v", result.Waiting)
	}
	if result.Atomics.Type != "woke" || result.Atomics.Status != "ok" || result.Atomics.Value != 77 {
		t.Fatalf("Atomics 唤醒=%+v", result.Atomics)
	}
	if result.Error.Message != "worker-boom" {
		t.Fatalf("worker 异常=%+v", result.Error)
	}
	if result.Main != 7 {
		t.Fatalf("主线在 worker 异常后未继续: %d", result.Main)
	}
}
