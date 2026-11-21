package guest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
)

const (
	q25PureDigest   = "bd08adbdafbc08b07db404283a88e6f6b1e4fb27811f163174a19cfea79da925"
	q25NativeDigest = "8aa0256f89e84735a07c37ef5c72a63dd1ff4785fa955c6590fbb3aec6f9c29c"
)

func runNodeInDir(t *testing.T, connection *instance.Connection, dir string, args []string, timeout time.Duration, extra map[string]string) (string, string, uint8) {
	t.Helper()
	output := &bytes.Buffer{}
	diagnostic := &bytes.Buffer{}
	env := map[string]string{
		"PATH": "/usr/bin:/bin", "HOME": "/work", "LANG": "C.UTF-8",
		"NODE_ICU_DATA": "/usr/share/icu/76.1",
	}
	for name, value := range extra {
		env[name] = value
	}
	runCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	process, err := connection.Exec(runCtx, instance.ExecSpec{
		Program: "/usr/bin/node",
		Args:    args,
		Dir:     dir,
		Env:     env,
		Streams: fd.Streams{Output: output, Error: diagnostic},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := connection.Wait(runCtx, process.ID)
	if err != nil {
		t.Fatalf("node %v: %v (stdout=%q stderr=%q)", args, err, output.String(), diagnostic.String())
	}
	return output.String(), diagnostic.String(), state.ExitCode
}

// TestNode22NpmOfflineInstall 验证 Q25:客体 npm 使用锁文件和本地
// 归档离线安装纯 JS 包与预构建原生扩展,执行 postinstall 脚本并
// 由客体 Node 解析执行。
func TestNode22NpmOfflineInstall(t *testing.T) {
	machine, connection := node22RealtimeGuest(t)
	installGuestShell(t, machine)
	installNpm(t, machine)
	pure := python312Asset(t, "testdata/nodejs/q25/q25-pure-1.0.0.tgz", q25PureDigest)
	native := python312Asset(t, "testdata/nodejs/q25/q25-native-1.0.0.tgz", q25NativeDigest)
	writeGuestFile(t, machine, "/work/q25/q25-pure-1.0.0.tgz", pure, 0o644)
	writeGuestFile(t, machine, "/work/q25/q25-native-1.0.0.tgz", native, 0o644)
	writeGuestFile(t, machine, "/work/q25/package.json", []byte(`{
  "name": "q25-app",
  "version": "1.0.0",
  "private": true,
  "dependencies": {
    "q25-pure": "file:q25-pure-1.0.0.tgz",
    "q25-native": "file:q25-native-1.0.0.tgz"
  }
}
`), 0o644)
	writeGuestFile(t, machine, "/work/q25/package-lock.json", []byte(`{
  "name": "q25-app",
  "version": "1.0.0",
  "lockfileVersion": 3,
  "requires": true,
  "packages": {
    "": {
      "name": "q25-app",
      "version": "1.0.0",
      "dependencies": {
        "q25-pure": "file:q25-pure-1.0.0.tgz",
        "q25-native": "file:q25-native-1.0.0.tgz"
      }
    },
    "node_modules/q25-pure": {
      "version": "1.0.0",
      "resolved": "file:q25-pure-1.0.0.tgz",
      "license": "MIT"
    },
    "node_modules/q25-native": {
      "version": "1.0.0",
      "resolved": "file:q25-native-1.0.0.tgz",
      "license": "MIT",
      "hasInstallScript": true
    }
  }
}
`), 0o644)
	npmEnv := map[string]string{
		"npm_config_cache": "/work/q25-cache", "npm_config_offline": "true",
		"npm_config_audit": "false", "npm_config_fund": "false",
	}
	installOutput, installErr, code := runNodeInDir(t, connection, "/work/q25",
		[]string{"--jitless", "/usr/lib/node_modules/npm/bin/npm-cli.js", "ci", "--offline", "--no-audit", "--no-fund"},
		1800*time.Second, npmEnv)
	if code != 0 {
		t.Fatalf("npm ci exit=%d stdout=%q stderr=%q", code, installOutput, installErr)
	}
	script := `const fs = require("node:fs");
const pure = require("q25-pure");
const native = require("q25-native");
const marker = fs.readFileSync("node_modules/q25-native/installed.txt", "utf8").trim();
process.stdout.write(JSON.stringify({
  pureValue: pure.value,
  pureLabel: pure.label,
  nativeAdd: native.add(2, 3),
  marker,
}) + "\n");
`
	writeGuestFile(t, machine, "/work/q25/verify.js", []byte(script), 0o644)
	output, diagnostic, code := runNodeInDir(t, connection, "/work/q25",
		[]string{"--jitless", "/work/q25/verify.js"}, 240*time.Second, nil)
	if code != 0 {
		t.Fatalf("npm 验证 exit=%d stdout=%q stderr=%q (install=%q)", code, output, diagnostic, installOutput+installErr)
	}
	var result struct {
		PureValue int    `json:"pureValue"`
		PureLabel string `json:"pureLabel"`
		NativeAdd int    `json:"nativeAdd"`
		Marker    string `json:"marker"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("npm 输出非法: %v (%q)", err, output)
	}
	if result.PureValue != 21 || result.PureLabel != "q25-pure" || result.NativeAdd != 5 || result.Marker != "q25-native-postinstall" {
		t.Fatalf("npm 安装结果=%+v", result)
	}
}
