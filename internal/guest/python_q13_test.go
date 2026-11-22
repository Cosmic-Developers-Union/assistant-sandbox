package guest_test

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	q13PkgDigest = "d1a5884ac4b0eb03e49bce38cbf4d18df9d93b07f4fc75216544c4d969b51ccf"
	q13BadDigest = "0cfa6336930576b67c44968d37ebd9a980ee2f76d1469e8e77c70c0469554695"
)

// TestPython312ControlledPipInstall 验证 Q13:客体 pip 经 CA 校验的
// HTTPS 受控索引安装固定 wheel,网络策略生效,坏哈希失败且不留下可
// 导入包。
func TestPython312ControlledPipInstall(t *testing.T) {
	dnsPort := startGuestDNS(t)
	ca := newTestCA(t, "Guest Package CA", 31)
	certificate := ca.issueServer(t, []string{"pypi.test"}, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour), 32)
	good := python312Asset(t, "testdata/python312/q13/q13_pkg-1.0-py3-none-any.whl", q13PkgDigest)
	bad := python312Asset(t, "testdata/python312/q13/q13_bad-1.0-py3-none-any.whl", q13BadDigest)
	mux := http.NewServeMux()
	var requestMu sync.Mutex
	var requests []string
	recordRequest := func(request *http.Request) {
		requestMu.Lock()
		defer requestMu.Unlock()
		requests = append(requests, request.URL.Path)
	}
	serveWheel := func(writer http.ResponseWriter, request *http.Request, name string, content []byte) {
		recordRequest(request)
		http.ServeContent(writer, request, name, time.Now(), bytes.NewReader(content))
	}
	mux.HandleFunc("/simple/q13-pkg/", func(writer http.ResponseWriter, request *http.Request) {
		recordRequest(request)
		_, _ = fmt.Fprintf(writer, `<html><body><a href="../../packages/q13_pkg-1.0-py3-none-any.whl#sha256=%s">q13_pkg-1.0-py3-none-any.whl</a></body></html>`+"\n", q13PkgDigest)
	})
	mux.HandleFunc("/simple/q13-bad/", func(writer http.ResponseWriter, request *http.Request) {
		recordRequest(request)
		_, _ = fmt.Fprintf(writer, `<html><body><a href="../../packages/q13_bad-1.0-py3-none-any.whl#sha256=%s">q13_bad-1.0-py3-none-any.whl</a></body></html>`+"\n", strings.Repeat("0", 64))
	})
	mux.HandleFunc("/packages/q13_pkg-1.0-py3-none-any.whl", func(writer http.ResponseWriter, request *http.Request) {
		serveWheel(writer, request, "q13_pkg-1.0-py3-none-any.whl", good)
	})
	mux.HandleFunc("/packages/q13_bad-1.0-py3-none-any.whl", func(writer http.ResponseWriter, request *http.Request) {
		serveWheel(writer, request, "q13_bad-1.0-py3-none-any.whl", bad)
	})
	hostPort := startGuestHTTPS(t, certificate, mux)
	machine, connection := python312GuestWith(t, guestEgress(tlsEgressPolicy(dnsPort, map[uint16]uint16{443: hostPort})))
	env := installGuestCA(t, machine, ca.pem)

	probe := `import socket, ssl, urllib.request
context = ssl.create_default_context(cafile="/etc/ssl/certs/ca-certificates.crt")
data = urllib.request.urlopen("https://pypi.test/simple/q13-pkg/", context=context, timeout=30).read()
print("Q13-HTTP", data[:20])
blocked = socket.socket()
blocked.settimeout(5)
try:
    blocked.connect(("10.0.2.2", 80))
except OSError:
    pass
else:
    raise AssertionError("unmapped egress port 80 was allowed")
finally:
    blocked.close()
print("Q13-POLICY")
`
	output, code := runPython312(t, connection, probe, 180*time.Second)
	if code != 0 || !strings.Contains(output, "Q13-HTTP") || !strings.Contains(output, "Q13-POLICY") {
		t.Fatalf("HTTPS 索引探针 exit=%d output=%q", code, output)
	}
	extractPython312PipTo(t, machine, python312Asset(t, "testdata/python312/python312-pip.tar.gz", python312PipDigest),
		"/usr/lib/python3.12/site-packages")

	script := `import os, sys

def mark(value):
    with open("/work/q13.status", "w") as output:
        output.write(value + "\n")
        output.flush()
mark("start")
os.environ.update({"PATH": "/usr/bin:/bin", "HOME": "/work", "LANG": "C.UTF-8",
                   "PIP_CERT": "` + env["SSL_CERT_FILE"] + `",
                   "SSL_CERT_FILE": "` + env["SSL_CERT_FILE"] + `",
                   "PIP_NO_CACHE_DIR": "1", "PIP_DISABLE_PIP_VERSION_CHECK": "1",
                   "PIP_NO_COLOR": "1", "PIP_PROGRESS_BAR": "off",
                   "PIP_DEFAULT_TIMEOUT": "30", "PIP_VERBOSE": "1"})
sys.path.insert(0, "/usr/lib/python3.12/site-packages")
from pip._internal.cli.main import main as pip_main

def run_pip(args):
    try:
        return int(pip_main(args) or 0)
    except SystemExit as error:
        return int(error.code or 0)

index = "https://pypi.test/simple"
mark("good")
good = run_pip(["install", "--index-url", index, "--target", "/work/q13site",
                "--no-deps", "--only-binary=:all:", "--no-compile",
                "--timeout", "30", "--retries", "0", "--verbose", "q13-pkg"])
mark("good-done")
print("install", good, flush=True)
assert good == 0
sys.path.insert(0, "/work/q13site")
import q13_pkg
assert q13_pkg.answer() == 42
sys.path.remove("/work/q13site")
mark("bad")
bad = run_pip(["install", "--index-url", index, "--target", "/work/q13bad",
               "--no-deps", "--only-binary=:all:", "--no-compile",
               "--timeout", "30", "--retries", "0", "--verbose", "q13-bad"])
print("bad", bad, flush=True)
assert bad != 0
mark("bad-done")
sys.path.insert(0, "/work/q13bad")
try:
    import q13_bad
except ImportError:
    pass
else:
    raise AssertionError("bad wheel left an importable package")
print("Q13-OK", flush=True)
os._exit(0)
`
	output, code, runErr := runPython312Outcome(t, connection, script, 1800*time.Second, nil)
	if runErr != nil || code != 0 || !strings.Contains(output, "Q13-OK") {
		status, _ := readGuestFile(machine, "/work/q13.status")
		requestMu.Lock()
		seen := append([]string(nil), requests...)
		requestMu.Unlock()
		t.Fatalf("pip 安装 exit=%d status=%q requests=%v output=%q err=%v",
			code, status, seen, output, runErr)
	}
}
