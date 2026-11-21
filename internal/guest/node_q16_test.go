package guest_test

import (
	"strings"
	"testing"
	"time"
)

// TestNode22MinimalStartup 验证 Q16:客体执行真实 riscv64 Node.js,
// 不使用宿主解释器或宿主代理。Node 22.23.5 是当前可稳定退出的固定
// 运行时;Node 24.22.3 的退出期缺陷另见 TODO 说明。
func TestNode22MinimalStartup(t *testing.T) {
	machine, connection := node22Guest(t)
	output, diagnostic, code := runNode22(t, connection, []string{"--jitless", "--version"}, 180*time.Second)
	if code != 0 || output != "v22.23.5\n" {
		t.Fatalf("node --version exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}

	script := `const values = [1 + 2 * 3, 7 / 2, Math.hypot(3, 4), (2n ** 64n).toString()];
const result = {
  node: process.version,
  arch: process.arch,
  execPath: process.execPath,
  values,
};
process.stdout.write(JSON.stringify(result) + "\n");
`
	writeGuestFile(t, machine, "/work/q16_arithmetic.js", []byte(script), 0o644)
	output, diagnostic, code = runNode22(t, connection, []string{"--jitless", "/work/q16_arithmetic.js"}, 240*time.Second)
	if code != 0 {
		t.Fatalf("node 脚本 exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}
	want := `{"node":"v22.23.5","arch":"riscv64","execPath":"/usr/bin/node","values":[7,3.5,5,"18446744073709551616"]}` + "\n"
	if output != want {
		t.Fatalf("node 脚本 stdout=%q want %q (stderr=%q)", output, want, diagnostic)
	}
	if strings.Contains(output, "host") {
		t.Fatalf("输出不应来自宿主代理: %q", output)
	}
}
