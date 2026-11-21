package guest_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

const (
	guestTLSValidPort     = 443
	guestTLSExpiredPort   = 444
	guestTLSWrongNamePort = 445
	guestTLSUntrustedPort = 446
)

// tlsAcceptanceServers 启动 DNS 与四个宿主 TLS 服务:有效证书、过期
// 证书、错误域名证书和不受信 CA 签发的证书。
func tlsAcceptanceServers(t *testing.T) (uint16, map[uint16]uint16, []byte) {
	t.Helper()
	dnsPort := startGuestDNS(t)
	now := time.Now()
	trusted := newTestCA(t, "Guest Acceptance CA", 1)
	untrusted := newTestCA(t, "Untrusted CA", 2)
	ports := map[uint16]uint16{
		guestTLSValidPort: startTLSServer(t, trusted.issueServer(t,
			[]string{"tls.test"}, now.Add(-time.Hour), now.Add(24*time.Hour), 10)),
		guestTLSExpiredPort: startTLSServer(t, trusted.issueServer(t,
			[]string{"tls.test"}, now.Add(-25*time.Hour), now.Add(-time.Hour), 11)),
		guestTLSWrongNamePort: startTLSServer(t, trusted.issueServer(t,
			[]string{"other.test"}, now.Add(-time.Hour), now.Add(24*time.Hour), 12)),
		guestTLSUntrustedPort: startTLSServer(t, untrusted.issueServer(t,
			[]string{"tls.test"}, now.Add(-time.Hour), now.Add(24*time.Hour), 13)),
	}
	return dnsPort, ports, trusted.pem
}

// TestPython312TLSValidation 验证 M11 的 Python 路径:客体 CA 存储
// 生效,有效证书成功,过期、错误域名和不受信证书分别被拒绝。
func TestPython312TLSValidation(t *testing.T) {
	dnsPort, ports, caPEM := tlsAcceptanceServers(t)
	machine, connection := python312GuestWith(t, guestEgress(tlsEgressPolicy(dnsPort, ports)))
	env := installGuestCA(t, machine, caPEM)
	script := `import json, socket, ssl

context = ssl.create_default_context()
def attempt(port):
    try:
        with socket.create_connection(("tls.test", port), timeout=20) as raw:
            with context.wrap_socket(raw, server_hostname="tls.test") as secure:
                return {"ok": secure.recv(32).decode().strip()}
    except ssl.SSLCertVerificationError as error:
        return {"verify": error.verify_code}
    except Exception as error:
        return {"error": type(error).__name__, "message": str(error)}

result = {
    "cafile": ssl.get_default_verify_paths().cafile,
    "check": context.check_hostname,
    "valid": attempt(443),
    "expired": attempt(444),
    "wrong": attempt(445),
    "untrusted": attempt(446),
}
print(json.dumps(result, sort_keys=True))
`
	output, code := runPython312Env(t, connection, script, 240*time.Second, env)
	if code != 0 {
		t.Fatalf("Python TLS exit=%d output=%q", code, output)
	}
	var result struct {
		CAFile    string               `json:"cafile"`
		Check     bool                 `json:"check"`
		Valid     struct{ OK string }  `json:"valid"`
		Expired   struct{ Verify int } `json:"expired"`
		Wrong     struct{ Verify int } `json:"wrong"`
		Untrusted struct{ Verify int } `json:"untrusted"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Python TLS 输出非法: %v (%q)", err, output)
	}
	if result.CAFile != "/etc/ssl/certs/ca-certificates.crt" || !result.Check {
		t.Fatalf("Python CA 配置未生效: %+v", result)
	}
	if result.Valid.OK != "secure" {
		t.Fatalf("有效证书未通过: %+v", result.Valid)
	}
	if result.Expired.Verify != 10 {
		t.Fatalf("过期证书 verify_code=%d want 10", result.Expired.Verify)
	}
	if result.Wrong.Verify != 62 {
		t.Fatalf("错误域名 verify_code=%d want 62", result.Wrong.Verify)
	}
	switch result.Untrusted.Verify {
	case 18, 19, 20, 21:
	default:
		t.Fatalf("不受信证书 verify_code=%d want 18/19/20/21", result.Untrusted.Verify)
	}
}

// TestNode22TLSValidation 验证 M11 的 Node 路径:NODE_EXTRA_CA_CERTS
// 配置的客体 CA 生效,有效证书成功,三类失败证书均返回可诊断错误。
func TestNode22TLSValidation(t *testing.T) {
	dnsPort, ports, caPEM := tlsAcceptanceServers(t)
	machine, connection := node22GuestWith(t, guestEgress(tlsEgressPolicy(dnsPort, ports)))
	installGuestCA(t, machine, caPEM)
	script := `const tls = require("node:tls");

function attempt(port) {
  return new Promise((resolve) => {
    let settled = false;
    const socket = tls.connect({
      host: "tls.test",
      port,
      servername: "tls.test",
      rejectUnauthorized: true,
      family: 4,
    });
    const finish = (value) => {
      if (settled) return;
      settled = true;
      resolve(value);
      socket.destroy();
    };
    socket.setTimeout(20000, () => finish({ timeout: true }));
    socket.once("secureConnect", () => {
      socket.setEncoding("utf8");
      socket.once("data", (data) => finish({ ok: data.trim() }));
    });
    socket.once("error", (error) => finish({ code: error.code, message: error.message }));
  });
}

(async () => {
  const result = {
    valid: await attempt(443),
    expired: await attempt(444),
    wrong: await attempt(445),
    untrusted: await attempt(446),
  };
  process.stdout.write(JSON.stringify(result) + "\n");
})().catch((error) => {
  process.stderr.write(String(error && error.stack || error) + "\n");
  process.exitCode = 1;
});
`
	writeGuestFile(t, machine, "/work/m11_tls.js", []byte(script), 0o644)
	output, diagnostic, code := runNode22Env(t, connection, []string{"--jitless", "/work/m11_tls.js"}, 360*time.Second,
		map[string]string{"NODE_EXTRA_CA_CERTS": "/etc/ssl/certs/ca-certificates.crt"})
	if code != 0 {
		t.Fatalf("Node TLS exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}
	var result map[string]map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Node TLS 输出非法: %v (%q)", err, output)
	}
	if result["valid"]["ok"] != "secure" {
		t.Fatalf("有效证书未通过: %+v", result["valid"])
	}
	want := map[string]string{
		"expired":   "CERT_HAS_EXPIRED",
		"wrong":     "ERR_TLS_CERT_ALTNAME_INVALID",
		"untrusted": "UNABLE_TO_VERIFY_LEAF_SIGNATURE",
	}
	for name, code := range want {
		if got := fmt.Sprint(result[name]["code"]); got != code {
			t.Fatalf("%s 错误码=%q want %q (stderr=%q)", name, got, code, diagnostic)
		}
	}
}
