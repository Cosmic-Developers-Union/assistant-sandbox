package guest_test

import (
	"strings"
	"testing"
	"time"
)

// TestPython312DNSResolvesThroughPolicy 验证 M10 的 Python 路径:
// 客体 CPython 经 /etc/resolv.conf、musl getaddrinfo 与受策略约束的
// UDP 宿主出口解析成功,并对不存在域名得到 EAI_NONAME。
func TestPython312DNSResolvesThroughPolicy(t *testing.T) {
	hostPort := startGuestDNS(t)
	_, connection := python312GuestWith(t, guestEgress(dnsEgressPolicy(hostPort)))
	script := `import json, socket

result = {}
result["lookup"] = socket.getaddrinfo("guest.test", 80, socket.AF_INET, socket.SOCK_STREAM)[0][4][0]
result["host"] = socket.gethostbyname("guest.test")
try:
    socket.getaddrinfo("missing.test", 80, socket.AF_INET, socket.SOCK_STREAM)
    result["missing"] = "resolved"
except socket.gaierror as error:
    result["missing"] = error.errno == socket.EAI_NONAME
print(json.dumps(result))
`
	output, code := runPython312(t, connection, script, 180*time.Second)
	if code != 0 || output != `{"lookup": "10.0.2.2", "host": "10.0.2.2", "missing": true}`+"\n" {
		t.Fatalf("Python DNS exit=%d output=%q", code, output)
	}
}

// TestNode22DNSResolvesThroughPolicy 验证 M10 的 Node 路径:dns.lookup
// (libuv/getaddrinfo)与 dns.resolve4(c-ares)都经受策略约束的 UDP
// 宿主出口解析,成功与 NXDOMAIN 均可区分。
func TestNode22DNSResolvesThroughPolicy(t *testing.T) {
	hostPort := startGuestDNS(t)
	machine, connection := node22GuestWith(t, guestEgress(dnsEgressPolicy(hostPort)))
	_ = machine
	script := `const dns = require("node:dns");

(async () => {
  const result = {};
  result.lookup = await new Promise((resolve, reject) => {
    dns.lookup("guest.test", { family: 4 }, (error, address, family) => {
      if (error) return reject(error);
      resolve({ address, family });
    });
  });
  result.resolved = await dns.promises.resolve4("guest.test");
  result.byName = await dns.promises.lookup("guest.test", { family: 4 });
  try {
    await dns.promises.lookup("missing.test", { family: 4 });
    result.missing = "resolved";
  } catch (error) {
    result.missing = error.code;
  }
  try {
    await dns.promises.resolve4("missing.test");
    result.resolveMissing = "resolved";
  } catch (error) {
    result.resolveMissing = error.code;
  }
  process.stdout.write(JSON.stringify(result) + "\n");
})().catch((error) => {
  process.stderr.write(String(error && error.stack || error) + "\n");
  process.exitCode = 1;
});
`
	writeGuestFile(t, machine, "/work/m10_dns.js", []byte(script), 0o644)
	output, diagnostic, code := runNode22(t, connection, []string{"--jitless", "/work/m10_dns.js"}, 300*time.Second)
	if code != 0 {
		t.Fatalf("Node DNS exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}
	want := `{"lookup":{"address":"10.0.2.2","family":4},"resolved":["10.0.2.2"],"byName":{"address":"10.0.2.2","family":4},"missing":"ENOTFOUND","resolveMissing":"ENOTFOUND"}` + "\n"
	if output != want {
		t.Fatalf("Node DNS stdout=%q want %q (stderr=%q)", output, want, diagnostic)
	}
	if strings.Contains(diagnostic, "Error:") {
		t.Fatalf("Node DNS 诊断含错误: %q", diagnostic)
	}
}
