package guest_test

import (
	"testing"
	"time"
)

// TestNode22FileAndPathModules 验证 Q18:同步/异步文件读写、
// 目录遍历、重命名与错误码,且异步回调全部到达。
func TestNode22FileAndPathModules(t *testing.T) {
	machine, connection := node22Guest(t)
	script := `const fs = require("node:fs");
const fsp = require("node:fs/promises");
const path = require("node:path");

(async () => {
  const dir = "/work/q18";
  fs.rmSync(dir, { recursive: true, force: true });
  fs.mkdirSync(path.join(dir, "sub"), { recursive: true });
  fs.writeFileSync(path.join(dir, "a.txt"), "alpha");
  fs.appendFileSync(path.join(dir, "a.txt"), "-beta");
  const syncRead = fs.readFileSync(path.join(dir, "a.txt"), "utf8");

  await fsp.writeFile(path.join(dir, "sub", "b.txt"), "gamma");
  const asyncRead = await fsp.readFile(path.join(dir, "sub", "b.txt"), "utf8");
  const entries = fs.readdirSync(dir, { withFileTypes: true })
    .map((entry) => [entry.name, entry.isDirectory()])
    .sort();
  const subEntries = await fsp.readdir(path.join(dir, "sub"));
  fs.renameSync(path.join(dir, "a.txt"), path.join(dir, "renamed.txt"));

  let success = "";
  let missing = "";
  await new Promise((resolve, reject) => {
    fs.readFile(path.join(dir, "renamed.txt"), "utf8", (error, data) => {
      if (error) return reject(error);
      success = data;
      fs.readFile(path.join(dir, "missing.txt"), (error2) => {
        missing = error2 && error2.code;
        resolve();
      });
    });
  });

  const stat = await fsp.stat(path.join(dir, "sub"));
  let mkdirError = "";
  try {
    fs.mkdirSync(path.join(dir, "sub"));
  } catch (error) {
    mkdirError = error.code;
  }
  const result = {
    syncRead,
    asyncRead,
    entries,
    subEntries,
    renamed: fs.existsSync(path.join(dir, "renamed.txt")),
    success,
    missing,
    subIsDirectory: stat.isDirectory(),
    mkdirError,
  };
  process.stdout.write(JSON.stringify(result) + "\n");
})().catch((error) => {
  process.stderr.write(String(error && error.stack || error) + "\n");
  process.exitCode = 1;
});
`
	writeGuestFile(t, machine, "/work/q18_file_path.js", []byte(script), 0o644)
	output, diagnostic, code := runNode22(t, connection, []string{"--jitless", "/work/q18_file_path.js"}, 300*time.Second)
	if code != 0 {
		t.Fatalf("文件/路径用例 exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}
	want := `{"syncRead":"alpha-beta","asyncRead":"gamma","entries":[["a.txt",false],["sub",true]],"subEntries":["b.txt"],"renamed":true,"success":"alpha-beta","missing":"ENOENT","subIsDirectory":true,"mkdirError":"EEXIST"}` + "\n"
	if output != want {
		t.Fatalf("文件/路径用例 stdout=%q want %q (stderr=%q)", output, want, diagnostic)
	}
}
