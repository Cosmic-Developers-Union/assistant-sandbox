package guest_test

import (
	"strings"
	"testing"
	"time"
)

// TestNode22JITlessExecutionConfig 验证 A05 阶段一的 V8 jitless 配置:
// 语言执行不依赖可执行数据页,WebAssembly 明确不可用,进程映射中没有
// RWX 页。受控 JIT 的最终配置不在本阶段冒充已启用。
func TestNode22JITlessExecutionConfig(t *testing.T) {
	machine, connection := node22RealtimeGuest(t)
	script := `const fs = require("node:fs");
let total = 0;
for (let i = 0; i < 10000; i++) total = (total + i) | 0;
if (total !== 49995000) throw new Error("arithmetic");
let wasm;
try {
  if (typeof WebAssembly === "undefined") throw new Error("WebAssembly unavailable");
  new WebAssembly.Module(new Uint8Array([0,97,115,109,1,0,0,0]));
  wasm = "unexpected";
} catch (error) {
  wasm = "disabled";
}
const maps = fs.readFileSync("/proc/self/maps", "utf8");
if (maps.split("\n").some((line) => /\brwx\b/i.test(line))) throw new Error("rwx mapping");
process.stdout.write("Q24-JITLESS " + total + " " + wasm + "\n");
`
	writeGuestFile(t, machine, "/work/q24-jitless.js", []byte(script), 0o644)
	output, diagnostic, code := runNode22(t, connection, []string{"--jitless", "/work/q24-jitless.js"}, 300*time.Second)
	if code != 0 || !strings.Contains(output, "Q24-JITLESS 49995000 disabled") {
		t.Fatalf("jitless exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}
}
