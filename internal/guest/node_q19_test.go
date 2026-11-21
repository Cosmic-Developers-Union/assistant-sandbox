package guest_test

import (
	"encoding/json"
	"testing"
	"time"
)

// TestNode22EventLoop 验证 Q19:libuv 定时器、promise/nextTick、
// 并发文件与网络 I/O、关闭回调顺序,以及空闲等待不忙等。
func TestNode22EventLoop(t *testing.T) {
	machine, connection := node22RealtimeGuest(t)
	script := `const fs = require("node:fs");
const fsp = fs.promises;
const net = require("node:net");

(async () => {
  const started = Date.now();
  const events = [];
  const record = (name) => events.push({ name, at: Date.now() - started });
  process.nextTick(() => record("tick"));
  Promise.resolve().then(() => record("promise"));
  queueMicrotask(() => record("microtask"));
  setTimeout(() => record("timeout20"), 20);
  setTimeout(() => record("timeout500"), 500);
  setTimeout(() => record("timeout0"), 0);
  setImmediate(() => record("immediate"));
  await new Promise((resolve) => setTimeout(resolve, 700));

  let intervalTicks = 0;
  await new Promise((resolve) => {
    const timer = setInterval(() => {
      intervalTicks += 1;
      if (intervalTicks === 3) {
        clearInterval(timer);
        resolve();
      }
    }, 10);
  });

  await fsp.mkdir("/work/q19", { recursive: true });
  const files = Array.from({ length: 8 }, (_, index) => "/work/q19/f" + index + ".txt");
  await Promise.all(files.map((name, index) => fsp.writeFile(name, "data-" + index)));
  const contents = await Promise.all(files.map((name) => fsp.readFile(name, "utf8")));

  const server = net.createServer((socket) => {
    socket.on("data", (data) => socket.write("echo:" + data.toString()));
  });
  await new Promise((resolve) => server.listen(38123, "127.0.0.1", resolve));
  const clients = await Promise.all(Array.from({ length: 6 }, (_, index) => new Promise((resolve, reject) => {
    const socket = net.connect(38123, "127.0.0.1", () => socket.write("c" + index));
    socket.once("data", (data) => {
      const text = data.toString();
      socket.end();
      resolve(text);
    });
    socket.once("error", reject);
  })));
  const closed = await new Promise((resolve) => server.close(() => resolve(true)));

  const usage = process.cpuUsage();
  await new Promise((resolve) => setTimeout(resolve, 500));
  const delta = process.cpuUsage(usage);
  const idleMs = (delta.user + delta.system) / 1000;
  const result = { events, intervalTicks, contents, clients, closed, idleMs };
  process.stdout.write(JSON.stringify(result) + "\n");
})().catch((error) => {
  process.stderr.write(String(error && error.stack || error) + "\n");
  process.exitCode = 1;
});
`
	writeGuestFile(t, machine, "/work/q19_event_loop.js", []byte(script), 0o644)
	output, diagnostic, code := runNode22(t, connection, []string{"--jitless", "/work/q19_event_loop.js"}, 240*time.Second)
	if code != 0 {
		t.Fatalf("事件循环 exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}
	var result struct {
		Events []struct {
			Name string `json:"name"`
			At   int64  `json:"at"`
		} `json:"events"`
		IntervalTicks int      `json:"intervalTicks"`
		Contents      []string `json:"contents"`
		Clients       []string `json:"clients"`
		Closed        bool     `json:"closed"`
		IdleMS        float64  `json:"idleMs"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("事件循环输出非法: %v (%q)", err, output)
	}
	events := map[string]int64{}
	for _, event := range result.Events {
		events[event.Name] = event.At
	}
	names := make([]string, 0, len(result.Events))
	for _, event := range result.Events {
		names = append(names, event.Name)
	}
	if len(names) < 7 {
		t.Fatalf("事件顺序不完整: %v", names)
	}
	wantPrefix := []string{"tick", "promise", "microtask"}
	for index, want := range wantPrefix {
		if names[index] != want {
			t.Fatalf("事件顺序[%d]=%q want %q (all=%v)", index, names[index], want, names)
		}
	}
	for _, name := range []string{"immediate", "timeout0", "timeout20", "timeout500"} {
		if _, ok := events[name]; !ok {
			t.Fatalf("事件 %s 未执行: %v", name, names)
		}
	}
	if events["timeout20"] < 15 {
		t.Fatalf("20ms 定时器实际 %dms,过早触发", events["timeout20"])
	}
	if events["timeout500"] < 450 {
		t.Fatalf("500ms 定时器实际 %dms,过早触发", events["timeout500"])
	}
	if result.IntervalTicks != 3 {
		t.Fatalf("setInterval 次数=%d want 3", result.IntervalTicks)
	}
	if len(result.Contents) != 8 {
		t.Fatalf("并发读文件=%d", len(result.Contents))
	}
	for index, content := range result.Contents {
		if want := "data-" + string(rune('0'+index)); content != want {
			t.Fatalf("文件 %d=%q want %q", index, content, want)
		}
	}
	if len(result.Clients) != 6 {
		t.Fatalf("并发客户端=%d", len(result.Clients))
	}
	for index, reply := range result.Clients {
		if want := "echo:c" + string(rune('0'+index)); reply != want {
			t.Fatalf("客户端 %d=%q want %q", index, reply, want)
		}
	}
	if !result.Closed {
		t.Fatal("server.close 回调未执行")
	}
	if result.IdleMS > 200 {
		t.Fatalf("空闲 500ms 消耗 CPU %.1fms,存在忙等", result.IdleMS)
	}
}
