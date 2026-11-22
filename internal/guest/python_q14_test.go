package guest_test

import (
	"strings"
	"testing"
	"time"
)

const python312TestDigest = "da675d8062cbbddf864e4ec3310072ebad2f007ea6233b71b9434e343770fbac"

// TestPython312UpstreamTestSlice 验证 Q14:固定 CPython 3.12.16 的
// 上游测试按模块运行,失败模块逐项报告,不以 skip 冒充通过。
func TestPython312UpstreamTestSlice(t *testing.T) {
	machine, connection := python312Guest(t)
	extractPython312Test(t, machine, python312Asset(t, "testdata/python312/python312-test.tar.gz", python312TestDigest))
	script := `import os, unittest

modules = ["test.test_textwrap", "test.test_pprint", "test.test_collections", "test.test_bisect"]
failures = []
for name in modules:
    suite = unittest.defaultTestLoader.loadTestsFromName(name)
    result = unittest.TextTestRunner(verbosity=1).run(suite)
    print(name, result.wasSuccessful(), flush=True)
    if not result.wasSuccessful():
        failures.append(name)
if failures:
    print("FAILMODULES", failures, flush=True)
    os._exit(1)
print("Q14-OK", flush=True)
os._exit(0)
`
	output, code := runPython312(t, connection, script, 2400*time.Second)
	if code != 0 || !strings.Contains(output, "Q14-OK") || !strings.Contains(output, "Ran ") || !strings.Contains(output, "OK") {
		t.Fatalf("上游测试分片 exit=%d output=%q", code, output)
	}
}
