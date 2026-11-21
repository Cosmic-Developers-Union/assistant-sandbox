package guest_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// TestNode22HTTPAndTLS 验证 Q22:客体 HTTP/HTTPS 服务与客户端完成
// 固定并发、keep-alive 连接回收、超时和证书校验。
func TestNode22HTTPAndTLS(t *testing.T) {
	machine, connection := node22RealtimeGuest(t)
	cert := python312Asset(t, "testdata/python312/q07-cert.pem", python312TLSCertDigest)
	key := python312Asset(t, "testdata/python312/q07-key.pem", python312TLSKeyDigest)
	writeGuestFile(t, machine, "/work/q22-cert.pem", cert, 0o644)
	writeGuestFile(t, machine, "/work/q22-key.pem", key, 0o600)
	script := `const fs = require("node:fs");
const http = require("node:http");
const https = require("node:https");
const net = require("node:net");

function listen(server, port) {
  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(port, "127.0.0.1", resolve);
  });
}

function close(server) {
  return new Promise((resolve) => server.close(() => resolve(true)));
}

function get(options) {
  return new Promise((resolve, reject) => {
    const request = (options.protocol === "https:" ? https : http).get(options, (response) => {
      let body = "";
      response.setEncoding("utf8");
      response.on("data", (data) => { body += data; });
      response.on("end", () => resolve({ status: response.statusCode, body }));
    });
    request.on("error", reject);
  });
}

(async () => {
  const result = {};
  const netServer = net.createServer((socket) => {
    socket.on("data", (data) => socket.write("echo:" + data.toString()));
  });
  await listen(netServer, 39003);
  result.reuse = await new Promise((resolve, reject) => {
    const received = [];
    const socket = net.connect(39003, "127.0.0.1", () => socket.write("one"));
    socket.setEncoding("utf8");
    socket.on("data", (data) => {
      received.push(data);
      if (received.length === 1) socket.write("two");
      else if (received.length === 2) socket.write("three");
      else { socket.end(); resolve(received); }
    });
    socket.on("error", reject);
  });
  await close(netServer);

  const agent = new http.Agent({ keepAlive: true, maxSockets: 4 });
  let connections = 0;
  const server = http.createServer((request, response) => {
    if (request.url === "/slow") {
      setTimeout(() => response.end("slow"), 2000);
      return;
    }
    response.end("ok:" + request.url);
  });
  server.on("connection", () => { connections += 1; });
  await listen(server, 39001);

  const responses = await Promise.all(Array.from({ length: 20 }, (_, index) =>
    get({ host: "127.0.0.1", port: 39001, path: "/item/" + index, agent })));
  result.responses = responses.map((response) => response.body);
  result.connections = connections;
  result.freeSockets = Object.values(agent.freeSockets).flat().length;

  result.timeout = await new Promise((resolve) => {
    const request = http.get({ host: "127.0.0.1", port: 39001, path: "/slow", agent: false, timeout: 200 }, (response) => response.resume());
    request.on("timeout", () => request.destroy(new Error("timeout")));
    request.on("error", (error) => resolve(error.code || error.message));
  });

  agent.destroy();
  result.closed = await close(server);

  const tlsServer = https.createServer({
    cert: fs.readFileSync("/work/q22-cert.pem"),
    key: fs.readFileSync("/work/q22-key.pem"),
  }, (request, response) => response.end("secure:" + request.url));
  await listen(tlsServer, 39002);
  const ca = fs.readFileSync("/work/q22-cert.pem");
  result.secure = await get({
    protocol: "https:",
    host: "127.0.0.1",
    port: 39002,
    path: "/tls",
    servername: "guest.test",
    ca,
  });
  result.wrongName = await new Promise((resolve) => {
    const request = https.get({ host: "127.0.0.1", port: 39002, path: "/tls", servername: "wrong.test", ca, agent: false }, (response) => response.resume());
    request.on("error", (error) => resolve(error.code));
  });
  result.untrusted = await new Promise((resolve) => {
    const request = https.get({ host: "127.0.0.1", port: 39002, path: "/tls", servername: "guest.test", agent: false }, (response) => response.resume());
    request.on("error", (error) => resolve(error.code));
  });
  result.tlsClosed = await close(tlsServer);
  process.stdout.write(JSON.stringify(result) + "\n");
})().catch((error) => {
  process.stderr.write(String(error && error.stack || error) + "\n");
  process.exitCode = 1;
});
`
	writeGuestFile(t, machine, "/work/q22_http.js", []byte(script), 0o644)
	output, diagnostic, code := runNode22(t, connection, []string{"--jitless", "/work/q22_http.js"}, 360*time.Second)
	if code != 0 {
		t.Fatalf("HTTP/HTTPS exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}
	var result struct {
		Responses   []string `json:"responses"`
		Connections int      `json:"connections"`
		FreeSockets int      `json:"freeSockets"`
		Reuse       []string `json:"reuse"`
		Timeout     string   `json:"timeout"`
		Closed      bool     `json:"closed"`
		Secure      struct {
			Status int    `json:"status"`
			Body   string `json:"body"`
		} `json:"secure"`
		WrongName string `json:"wrongName"`
		Untrusted string `json:"untrusted"`
		TLSClosed bool   `json:"tlsClosed"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("HTTP/HTTPS 输出非法: %v (%q)", err, output)
	}
	if len(result.Responses) != 20 {
		t.Fatalf("并发响应=%d", len(result.Responses))
	}
	seen := map[string]bool{}
	for _, body := range result.Responses {
		seen[body] = true
	}
	for index := 0; index < 20; index++ {
		want := "ok:/item/" + fmt.Sprint(index)
		if !seen[want] {
			t.Fatalf("缺少响应 %q (all=%v)", want, result.Responses)
		}
	}
	if result.Connections < 1 || result.Connections > 20 {
		t.Fatalf("HTTP 连接数=%d", result.Connections)
	}
	if result.FreeSockets < 1 {
		t.Fatal("keep-alive agent 未保留空闲连接")
	}
	if len(result.Reuse) != 3 || result.Reuse[0] != "echo:one" || result.Reuse[1] != "echo:two" || result.Reuse[2] != "echo:three" {
		t.Fatalf("单连接多次收发=%v", result.Reuse)
	}
	if result.Timeout == "" {
		t.Fatal("慢请求未触发超时")
	}
	if !result.Closed || !result.TLSClosed {
		t.Fatalf("server.close=%v tls.close=%v", result.Closed, result.TLSClosed)
	}
	if result.Secure.Status != 200 || result.Secure.Body != "secure:/tls" {
		t.Fatalf("HTTPS 成功响应=%+v", result.Secure)
	}
	if result.WrongName != "ERR_TLS_CERT_ALTNAME_INVALID" {
		t.Fatalf("错误域名错误码=%q", result.WrongName)
	}
	if result.Untrusted == "" || result.Untrusted == "ERR_TLS_CERT_ALTNAME_INVALID" {
		t.Fatalf("不受信证书错误码=%q", result.Untrusted)
	}
}
