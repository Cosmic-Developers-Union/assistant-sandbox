package guest_test

import (
	"strings"
	"testing"
	"time"
)

// TestNode22ModuleLoading 验证 Q17:CommonJS、ESM、package.json
// exports/main 与动态 import 均经客体真实文件解析。
func TestNode22ModuleLoading(t *testing.T) {
	machine, connection := node22Guest(t)
	files := map[string]string{
		"/work/q17/package.json": `{"type":"module"}` + "\n",
		"/work/q17/local.mjs":    "export const local = 7;\n",
		"/work/q17/legacy.cjs":   "module.exports = { legacy: 11 };\n",
		"/work/q17/main.mjs": `import { createRequire } from "node:module";
import pkgDefault, { kind } from "q17pkg";
import { local } from "./local.mjs";

const require = createRequire(import.meta.url);
const cjsPkg = require("q17pkg");
const legacy = require("q17legacy");
const cjsLocal = require("./legacy.cjs");
const extra = await import("q17pkg/extra");
const dynamicLocal = await import("./local.mjs");

const result = {
  kind,
  pkgDefault,
  cjsKind: cjsPkg.kind,
  cjsValue: cjsPkg.value,
  legacy: legacy.legacy,
  cjsLocal: cjsLocal.legacy,
  extra: extra.extra,
  local,
  dynamicLocal: dynamicLocal.local,
};
process.stdout.write(JSON.stringify(result) + "\n");
`,
		"/work/q17/node_modules/q17pkg/package.json": `{
  "name": "q17pkg",
  "version": "1.2.3",
  "type": "module",
  "exports": {
    ".": {"import": "./esm/index.js", "require": "./cjs/index.cjs"},
    "./extra": "./esm/extra.js"
  }
}
`,
		"/work/q17/node_modules/q17pkg/esm/index.js":    "export const kind = \"esm\";\nexport default 21;\n",
		"/work/q17/node_modules/q17pkg/esm/extra.js":    "export const extra = 5;\n",
		"/work/q17/node_modules/q17pkg/cjs/index.cjs":   "module.exports = { kind: \"cjs\", value: 22 };\n",
		"/work/q17/node_modules/q17legacy/package.json": `{"name":"q17legacy","version":"2.0.0","main":"lib/main.cjs"}` + "\n",
		"/work/q17/node_modules/q17legacy/lib/main.cjs": "module.exports = { legacy: 13 };\n",
	}
	for name, content := range files {
		writeGuestFile(t, machine, name, []byte(content), 0o644)
	}

	output, diagnostic, code := runNode22(t, connection, []string{"--jitless", "/work/q17/main.mjs"}, 300*time.Second)
	if code != 0 {
		t.Fatalf("模块加载 exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}
	want := `{"kind":"esm","pkgDefault":21,"cjsKind":"cjs","cjsValue":22,"legacy":13,"cjsLocal":11,"extra":5,"local":7,"dynamicLocal":7}` + "\n"
	if output != want {
		t.Fatalf("模块加载 stdout=%q want %q (stderr=%q)", output, want, diagnostic)
	}
	if strings.Contains(diagnostic, "Cannot find") || strings.Contains(diagnostic, "ERR_") {
		t.Fatalf("模块解析失败: %q", diagnostic)
	}
}
