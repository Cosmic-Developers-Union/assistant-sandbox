package guest_test

import (
	"context"
	"strings"
	"testing"
	"time"
)

const nodeGypToolchainDigest = "ff581db506abf7e4a29a8ffccadb44f9d2c46031391cad2084575859a3483e61"

// TestNode22NodeGypBuildsAddon 验证 Q30:客体 python3.12、TinyCC、GNU
// make 与固定 Node 头文件经 node-gyp 构建 N-API addon,再由客体 Node 加载。
func TestNode22NodeGypBuildsAddon(t *testing.T) {
	machine, connection := node22Guest(t)
	installGuestShell(t, machine)
	installNpm(t, machine)
	extractPython312(t, machine, python312Asset(t,
		"testdata/python312/python3-3.12.16-r0.apk", python312APKDigest))
	extractPython312Pyc(t, machine, python312Asset(t,
		"testdata/python312/python312-pyc.tar.gz", python312PycDigest))
	extractPython312Libs(t, machine, python312Asset(t,
		"testdata/python312/python312-libs.tar.gz", python312LibsDigest))
	disablePythonVFork(t, machine)
	extractGuestTar(t, machine, python312Asset(t,
		"testdata/tcc-riscv64-toolchain.tar.gz", tccToolchainDigest))
	extractGuestTar(t, machine, python312Asset(t,
		"testdata/nodejs/node-gyp-toolchain.tar.gz", nodeGypToolchainDigest))
	ctx := context.Background()
	for _, name := range []string{"cc", "gcc", "g++"} {
		if err := machine.Filesystem().Symlink(ctx, "tcc", "/usr/bin/"+name); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{
		"rm", "ln", "true", "env", "printf", "touch", "chmod", "dirname",
		"basename", "uname", "head", "tail", "mkdir", "sed", "cat", "cp",
		"wc", "grep", "ls", "find", "sort", "xargs",
	} {
		if err := machine.Filesystem().Symlink(ctx, "busybox", "/bin/"+name); err != nil {
			t.Fatal(err)
		}
	}

	addon := `#include <node_api.h>
#include <stddef.h>

static napi_value Add(napi_env env, napi_callback_info info) {
	size_t count = 2;
	napi_value argv[2];
	napi_get_cb_info(env, info, &count, argv, NULL, NULL);
	int32_t left = 0, right = 0;
	napi_get_value_int32(env, argv[0], &left);
	napi_get_value_int32(env, argv[1], &right);
	napi_value result;
	napi_create_int32(env, left + right, &result);
	return result;
}

NAPI_MODULE_INIT() {
	napi_value add;
	napi_create_function(env, "add", NAPI_AUTO_LENGTH, Add, NULL, &add);
	napi_set_named_property(env, exports, "add", add);
	return exports;
}
`
	binding := `{
  "targets": [{
    "target_name": "q30_addon",
    "sources": ["addon.c"],
    "cflags": ["-fPIC", "-O2"]
  }]
}
`
	writeGuestFile(t, machine, "/work/q30/addon.c", []byte(addon), 0o644)
	writeGuestFile(t, machine, "/work/q30/binding.gyp", []byte(binding), 0o644)
	makeWrapper := `import subprocess
import sys
subprocess._USE_VFORK = False
args = sys.argv[1:]
if args and args[0] == "--":
    args = args[1:]
workdir = None
if "-C" in args:
    workdir = args[args.index("-C") + 1]
result = subprocess.run(["/usr/bin/make", "-n", *args], capture_output=True, text=True)
if result.returncode != 0:
    print(result.stdout, end="")
    print(result.stderr, end="", file=sys.stderr)
    raise SystemExit(result.returncode)
for line in result.stdout.splitlines():
    if not line or line.startswith("make:") or line.startswith("touch "):
        continue
    if line.startswith("/usr/bin/tcc"):
        line = line.replace(" -Wl,--start-group", "").replace(" -Wl,--end-group", "").replace(" -pthread", "")
    subprocess.run(["/bin/bash", "-c", line], check=True, cwd=workdir)
`
	writeGuestFile(t, machine, "/work/q30/make-wrapper.py", []byte(makeWrapper), 0o644)
	launcher := `#include <unistd.h>
int main(int argc, char **argv) {
	char *args[64];
	int index = 0, source = 1;
	args[index++] = "/usr/bin/python3.12";
	args[index++] = "/work/q30/make-wrapper.py";
	args[index++] = "--";
	while (source < argc && index < 63) args[index++] = argv[source++];
	args[index] = 0;
	execv(args[0], args);
	return 127;
}
`
	writeGuestFile(t, machine, "/work/q30/make-wrapper.c", []byte(launcher), 0o644)
	launcherOut, launcherErr, launcherCode := runGuestProgram(t, connection, "/usr/bin/tcc",
		[]string{"-static", "-o", "/work/q30/make-wrapper", "/work/q30/make-wrapper.c"},
		map[string]string{"PATH": "/usr/bin:/bin", "HOME": "/work"}, 120*time.Second)
	if launcherCode != 0 {
		t.Fatalf("make wrapper compile exit=%d stdout=%q stderr=%q", launcherCode, launcherOut, launcherErr)
	}
	for _, compiler := range []string{"/usr/bin/cc", "/usr/bin/gcc", "/usr/bin/tcc"} {
		version, versionErr, versionCode := runGuestProgram(t, connection, compiler,
			[]string{"--version"}, map[string]string{"PATH": "/usr/bin:/bin", "HOME": "/work"}, 120*time.Second)
		if versionCode != 0 {
			t.Fatalf("%s --version exit=%d stdout=%q stderr=%q", compiler, versionCode, version, versionErr)
		}
	}
	direct, directErr, directCode := runGuestProgram(t, connection, "/usr/bin/tcc",
		[]string{"-c", "-fPIC", "-O2", "-I/work/node-headers/include/node",
			"-o", "/work/q30/direct.o", "/work/q30/addon.c"},
		map[string]string{"PATH": "/usr/bin:/bin", "HOME": "/work"}, 120*time.Second)
	if directCode != 0 {
		t.Fatalf("直接 TinyCC 编译 exit=%d stdout=%q stderr=%q", directCode, direct, directErr)
	}
	env := map[string]string{
		"PATH": "/usr/local/bin:/usr/bin:/bin", "HOME": "/work", "LANG": "C.UTF-8",
		"PYTHONHOME": "/usr", "npm_config_nodedir": "/work/node-headers",
		"npm_config_python": "/usr/bin/python3.12", "npm_config_jobs": "1",
		"V": "1", "CC": "/usr/bin/tcc", "CXX": "/usr/bin/tcc", "LD": "/usr/bin/tcc",
		"LDSHARED": "/usr/bin/tcc", "MAKE": "/work/q30/make-wrapper",
	}
	buildOut, buildErr, code := runNodeInDir(t, connection, "/work/q30",
		[]string{"--jitless", "/usr/lib/node_modules/npm/node_modules/node-gyp/bin/node-gyp.js",
			"rebuild", "--nodedir=/work/node-headers", "--python=/usr/bin/python3.12", "--jobs=1"},
		3600*time.Second, env)
	if code != 0 {
		t.Fatalf("node-gyp exit=%d stdout=%q stderr=%q", code, buildOut, buildErr)
	}
	verify := `const addon = require("/work/q30/build/Release/q30_addon.node");
if (addon.add(20, 22) !== 42) throw new Error("bad addon");
process.stdout.write("Q30-OK\n");
`
	writeGuestFile(t, machine, "/work/q30/verify.js", []byte(verify), 0o644)
	output, diagnostic, code := runNodeInDir(t, connection, "/work/q30",
		[]string{"--jitless", "/work/q30/verify.js"}, 240*time.Second, nil)
	if code != 0 || !strings.Contains(output, "Q30-OK") {
		t.Fatalf("node-gyp addon 加载 exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}
}
