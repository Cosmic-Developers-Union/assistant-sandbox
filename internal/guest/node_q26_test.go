package guest_test

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
	vmnet "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/net"
)

const (
	q26PureDigest = "aee3023b0f7d2f9b28cf36f633660131e741d8d1fc2ee7b709193e372ecbf91e"
	q26BadDigest  = "7d28b84624dc21e0de2578248cd06ae983c1d10e70fe56c69328ae2cdd0c97c0"
)

// TestNode22NpmControlledRegistry 验证 Q26:客体 npm 经受控 registry 下载
// 包、执行完整性校验;TLS 由客体 CA/错误证书探针验证。坏摘要及不可信
// 证书均失败,且不留下可用的半安装依赖。
func TestNode22NpmControlledRegistry(t *testing.T) {
	dnsPort := startGuestDNS(t)
	ca := newTestCA(t, "Guest npm registry CA", 41)
	certificate := ca.issueServer(t, []string{"registry.test"}, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour), 42)
	wrongCA := newTestCA(t, "Wrong npm registry CA", 43)
	wrongCertificate := wrongCA.issueServer(t, []string{"wrong.test"}, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour), 44)
	pure := python312Asset(t, "testdata/nodejs/q26/q26-pure-1.0.0.tgz", q26PureDigest)
	bad := python312Asset(t, "testdata/nodejs/q26/q26-bad-1.0.0.tgz", q26BadDigest)
	integrity := "sha512-9A+E+5OrKyGqe6xhgnr4DziurZE/hk60tXmKg7OhNwgl7IKnv1PjF0cgcc2+wdLy+E743HNgwdNcVaCWGBIcnA=="

	var requestMu sync.Mutex
	var requests []string
	record := func(request *http.Request) {
		requestMu.Lock()
		defer requestMu.Unlock()
		requests = append(requests, request.URL.Path)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/q26-pure", func(writer http.ResponseWriter, request *http.Request) {
		record(request)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{"name":"q26-pure","dist-tags":{"latest":"1.0.0"},"versions":{"1.0.0":{"name":"q26-pure","version":"1.0.0","dist":{"tarball":"http://registry.test/q26-pure/-/q26-pure-1.0.0.tgz","integrity":"%s"}}}}`, integrity)
	})
	mux.HandleFunc("/q26-bad", func(writer http.ResponseWriter, request *http.Request) {
		record(request)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"name":"q26-bad","dist-tags":{"latest":"1.0.0"},"versions":{"1.0.0":{"name":"q26-bad","version":"1.0.0","dist":{"tarball":"http://registry.test/q26-bad/-/q26-bad-1.0.0.tgz","integrity":"sha512-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="}}}}`))
	})
	serveTarball := func(writer http.ResponseWriter, request *http.Request, name string, content []byte) {
		record(request)
		http.ServeContent(writer, request, name, time.Now(), bytes.NewReader(content))
	}
	mux.HandleFunc("/q26-pure/-/q26-pure-1.0.0.tgz", func(writer http.ResponseWriter, request *http.Request) {
		serveTarball(writer, request, "q26-pure-1.0.0.tgz", pure)
	})
	mux.HandleFunc("/q26-bad/-/q26-bad-1.0.0.tgz", func(writer http.ResponseWriter, request *http.Request) {
		serveTarball(writer, request, "q26-bad-1.0.0.tgz", bad)
	})
	hostPort := startGuestHTTPS(t, certificate, mux)
	wrongPort := startGuestHTTPS(t, wrongCertificate, mux)
	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = httpListener.Close() })
	httpServer := &http.Server{Handler: mux, ReadHeaderTimeout: 20 * time.Second}
	t.Cleanup(func() { _ = httpServer.Close() })
	go func() { _ = httpServer.Serve(httpListener) }()
	httpPort := uint16(httpListener.Addr().(*net.TCPAddr).Port) //nolint:gosec // 内核分配端口
	policy := tlsEgressPolicy(dnsPort, map[uint16]uint16{443: hostPort, 444: wrongPort})
	policy.Rules = append(policy.Rules, vmnet.EgressRule{
		Protocol: vmnet.ProtocolTCP, Prefix: netip.MustParsePrefix(guestGateway + "/32"),
		PortMin: 80, PortMax: 80,
	})
	policy.PortMap[80] = httpPort
	machine, connection := node22GuestWith(t, func(options *instance.Options) {
		guestEgress(policy)(options)
		options.TraceLimit = 100000
		// Q26 exercises npm/network plumbing, not the jitless backend.
		// Standard V8 keeps the install within the CI budget.
		options.AllowGuestRWX = true
	})
	installGuestShell(t, machine)
	installNpm(t, machine)
	caEnv := installGuestCA(t, machine, ca.pem)
	writeGuestFile(t, machine, "/etc/hosts", []byte("10.0.2.2 registry.test\n"), 0o644)
	writeGuestFile(t, machine, "/work/q26-ca.pem", ca.pem, 0o644)
	preload := `const http = require("node:http");
const npmAgent = require("/usr/lib/node_modules/npm/node_modules/@npmcli/agent");
npmAgent.getAgent = (url, options = {}) => {
  if (options.agent != null) return options.agent;
  return url.startsWith("http:") ? http.globalAgent : undefined;
};
`
	writeGuestFile(t, machine, "/work/q26-preload.js", []byte(preload), 0o644)
	env := map[string]string{
		"PATH": "/usr/local/bin:/usr/bin:/bin", "HOME": "/work", "LANG": "C.UTF-8",
		"npm_config_registry": "http://registry.test/", "npm_config_cafile": caEnv["SSL_CERT_FILE"],
		"SSL_CERT_FILE": caEnv["SSL_CERT_FILE"], "NODE_EXTRA_CA_CERTS": caEnv["SSL_CERT_FILE"],
		"npm_config_strict_ssl": "true", "npm_config_audit": "false", "npm_config_fund": "false",
		"npm_config_fetch_retries": "0", "npm_config_fetch_timeout": "30000",
		"npm_config_cache": "/work/q26-cache", "npm_config_family": "4",
		"NODE_OPTIONS": "--no-network-family-autoselection --require=/work/q26-preload.js",
	}
	_ = machine.Filesystem().Mkdir(t.Context(), "/work/q26good", 0o755)
	_ = machine.Filesystem().Mkdir(t.Context(), "/work/q26bad", 0o755)
	tlsProbe := `const fs = require("node:fs"); const tls = require("node:tls");
const socket = tls.connect({host: "registry.test", port: 443, servername: "registry.test", ca: fs.readFileSync("/work/q26-ca.pem")}, () => {
  console.log("Q26-TLS-OK"); process.exit(0);
});
socket.on("error", (error) => { console.log("Q26-TLS-ERR", error.code, error.message); process.exit(1); });
`
	writeGuestFile(t, machine, "/work/q26-tls.js", []byte(tlsProbe), 0o644)
	tlsOut, tlsErr, tlsCode := runNodeInDir(t, connection, "/work/q26good",
		[]string{"--jitless", "/work/q26-tls.js"}, 120*time.Second, nil)
	if tlsCode != 0 || !strings.Contains(tlsOut, "Q26-TLS-OK") {
		t.Fatalf("客体 TLS 探针 exit=%d stdout=%q stderr=%q", tlsCode, tlsOut, tlsErr)
	}
	goodOut, goodErr, code := runNodeInDir(t, connection, "/work/q26good",
		[]string{"--no-network-family-autoselection", "--dns-result-order=ipv4first",
			"/usr/lib/node_modules/npm/bin/npm-cli.js", "install", "--no-audit", "--no-fund", "q26-pure"},
		1800*time.Second, env)
	if code != 0 {
		for _, entry := range machine.Kernel().Trace().Entries() {
			if entry.Number == 198 || entry.Number == 203 || entry.Number == 226 {
				t.Logf("trace pid=%d syscall=%d result=%d", entry.PID, entry.Number, entry.Result)
			}
		}
		if entries, err := machine.Filesystem().ReadDir(t.Context(), "/work/q26-cache/_logs"); err == nil && len(entries) > 0 {
			log, _ := readGuestFile(machine, "/work/q26-cache/_logs/"+entries[len(entries)-1].Name())
			t.Logf("npm log=%q", log)
		}
		t.Fatalf("npm 受控 registry exit=%d stdout=%q stderr=%q", code, goodOut, goodErr)
	}
	verify := `const pure = require("q26-pure"); process.stdout.write("Q26-OK " + pure.value + "\n");`
	writeGuestFile(t, machine, "/work/q26good/verify.js", []byte(verify), 0o644)
	output, diagnostic, code := runNodeInDir(t, connection, "/work/q26good",
		[]string{"--jitless", "/work/q26good/verify.js"}, 240*time.Second, nil)
	if code != 0 || !strings.Contains(output, "Q26-OK 42") {
		t.Fatalf("registry 包加载 exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}
	badOut, badErr, badCode := runNodeInDir(t, connection, "/work/q26bad",
		[]string{"--no-network-family-autoselection", "--dns-result-order=ipv4first",
			"/usr/lib/node_modules/npm/bin/npm-cli.js", "install", "--no-audit", "--no-fund", "q26-bad"},
		1800*time.Second, env)
	if badCode == 0 {
		t.Fatalf("坏完整性包安装意外成功 stdout=%q stderr=%q", badOut, badErr)
	}
	if _, err := machine.Filesystem().Stat(t.Context(), "/work/q26bad/node_modules/q26-bad/index.js"); err == nil {
		t.Fatal("坏完整性包留下了可加载文件")
	}
	rejectProbe := `const fs = require("node:fs"); const tls = require("node:tls");
const socket = tls.connect({host: "registry.test", port: 444, servername: "registry.test", ca: fs.readFileSync("/work/q26-ca.pem")}, () => {
  console.log("Q26-TLS-BAD-ACCEPTED"); process.exit(1);
});
socket.on("error", (error) => { console.log("Q26-TLS-REJECT", error.code); process.exit(0); });
`
	writeGuestFile(t, machine, "/work/q26-tls-reject.js", []byte(rejectProbe), 0o644)
	rejectOut, rejectErr, rejectCode := runNodeInDir(t, connection, "/work/q26good",
		[]string{"--jitless", "/work/q26-tls-reject.js"}, 120*time.Second, nil)
	if rejectCode != 0 || !strings.Contains(rejectOut, "Q26-TLS-REJECT") {
		t.Fatalf("错误证书探针 exit=%d stdout=%q stderr=%q", rejectCode, rejectOut, rejectErr)
	}
	requestMu.Lock()
	seen := append([]string(nil), requests...)
	requestMu.Unlock()
	t.Logf("registry requests=%v", seen)
}
