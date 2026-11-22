package guest_test

import (
	"strings"
	"testing"
	"time"
)

const (
	q29SdistDigest   = "e3a17dee8071bf701df09b4ba42cfc61f26ce8ab1e49056e90ec94cbd1761222"
	q29HeadersDigest = "4f785f92242b73112e50fa57aab111e4411e2bba4189ea08450f2c887bf17a01"
)

// TestPython312PipBuildsNativeSdist 验证 Q29:固定 sdist 的 PEP 517 后端
// 在客体调用 TinyCC 编译 Python C 扩展,pip 产出并安装 wheel,随后由
// 同一客体 Python 导入并确认 ELF 架构。
func TestPython312PipBuildsNativeSdist(t *testing.T) {
	machine, connection := python312Guest(t)
	installGuestShell(t, machine)
	disablePythonVFork(t, machine)
	extractGuestTar(t, machine, python312Asset(t,
		"testdata/tcc-riscv64-toolchain.tar.gz", tccToolchainDigest))
	extractGuestTar(t, machine, python312Asset(t,
		"testdata/python312/python312-dev-headers.tar.gz", q29HeadersDigest))
	extractPython312PipTo(t, machine, python312Asset(t,
		"testdata/python312/python312-pip.tar.gz", python312PipDigest),
		"/usr/lib/python3.12/site-packages")
	writeGuestFile(t, machine, "/work/q29_native-1.0.tar.gz",
		python312Asset(t, "testdata/python312/q29_native-1.0.tar.gz", q29SdistDigest), 0o644)

	script := `import os, struct, subprocess, sys, zipfile
subprocess._USE_VFORK = False

def mark(value):
    with open("/work/q29.status", "w") as output:
        output.write(value + "\n")
        output.flush()

mark("start")
os.environ.update({
    "PATH": "/usr/bin:/bin", "HOME": "/work", "LANG": "C.UTF-8",
    "PIP_NO_CACHE_DIR": "1", "PIP_DISABLE_PIP_VERSION_CHECK": "1",
    "PIP_NO_COLOR": "1", "PIP_PROGRESS_BAR": "off",
})
sys.path.insert(0, "/usr/lib/python3.12/site-packages")
from pip._internal.cli.main import main as pip_main

def run_pip(args):
    try:
        return int(pip_main(args) or 0)
    except SystemExit as error:
        return int(error.code or 0)

mark("install")
code = run_pip(["install", "--no-index", "--no-build-isolation", "--no-compile",
                "--target", "/work/q29site", "/work/q29_native-1.0.tar.gz"])
print("install", code, flush=True)
assert code == 0
assert os.path.exists("/work/q29-built.whl")
with zipfile.ZipFile("/work/q29-built.whl") as wheel:
    names = wheel.namelist()
assert "q29_native-1.0.dist-info/WHEEL" in names
with zipfile.ZipFile("/work/q29-built.whl") as wheel:
    tag = wheel.read("q29_native-1.0.dist-info/WHEEL").decode()
assert "Tag: cp312-cp312-linux_riscv64" in tag
mark("verify")
sys.path.insert(0, "/work/q29site")
import q29_native
assert q29_native.add(20, 22) == 42
with open(q29_native.__file__, "rb") as image:
    header = image.read(20)
assert header[:4] == b"\x7fELF"
assert header[4] == 2 and header[5] == 1
assert struct.unpack("<H", header[18:20])[0] == 243
print("Q29-OK", q29_native.__file__, flush=True)
os._exit(0)
`
	output, code, runErr := runPython312Outcome(t, connection, script, 900*time.Second, nil)
	if runErr != nil || code != 0 || !strings.Contains(output, "Q29-OK") {
		status, _ := readGuestFile(machine, "/work/q29.status")
		t.Fatalf("sdist 构建安装 exit=%d status=%q output=%q err=%v", code, status, output, runErr)
	}
}
