package guest_test

import (
	"strings"
	"testing"
	"time"
)

const (
	python312NativeDigest  = "3c6fc6345919c7fb6cd0e285c965e9d7dc807a24dc2dfba8c33604efd8120e06"
	python312BadArchDigest = "1677674219ceb9197514282ab8e7ae7dc61a9ed6cbe54bf1019167b8d0204779"
)

// TestPython312NativeExtension 验证 Q06:目标 ABI 的 CPython 扩展可在
// 客体 import 并调用;错误架构的产物必须被拒绝而不是执行。
func TestPython312NativeExtension(t *testing.T) {
	_, connection := python312Guest(t)
	native := python312Asset(t, "testdata/python312/q06_native.cpython-312-riscv64-linux-musl.so", python312NativeDigest)
	badArch := python312Asset(t, "testdata/python312/q06_bad.cpython-312-x86_64-linux-gnu.so", python312BadArchDigest)
	machine := connection.VM()
	writeGuestFile(t, machine, "/work/q06_native.cpython-312-riscv64-linux-musl.so", native, 0o755)
	writeGuestFile(t, machine, "/work/q06_bad.cpython-312-riscv64-linux-musl.so", badArch, 0o755)

	script := `import sys
sys.path.insert(0, "/work")
import q06_native
assert q06_native.add(2, 3) == 5
assert q06_native.add(-4, 10) == 6
print("Q06-OK", flush=True)
`
	output, code := runPython312(t, connection, script, 180*time.Second)
	if code != 0 || output != "Q06-OK\n" {
		t.Fatalf("原生扩展 exit=%d output=%q", code, output)
	}

	badScript := `import sys
sys.path.insert(0, "/work")
try:
    import q06_bad
except ImportError as error:
    print("Q06-BAD-REJECTED", type(error).__name__, flush=True)
else:
    raise SystemExit("wrong architecture extension was accepted")
`
	output, code = runPython312(t, connection, badScript, 180*time.Second)
	if code != 0 || !strings.Contains(output, "Q06-BAD-REJECTED ImportError") {
		t.Fatalf("错误架构扩展 exit=%d output=%q", code, output)
	}
}
