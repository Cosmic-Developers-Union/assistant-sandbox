package guest_test

import (
	"testing"
	"time"
)

// TestPython312Subprocess 验证 Q09:在客体启动真实 ELF,传递参数与
// 环境,收集双流输出,并处理超时、退出码与信号。
func TestPython312Subprocess(t *testing.T) {
	_, connection := python312Guest(t)
	script := `import os, signal, subprocess

environment = {
    "PATH": "/usr/bin:/bin",
    "HOME": "/work",
    "LANG": "C.UTF-8",
    "PYTHONHOME": "/usr",
    "Q09": "value",
}

child = (
    'import os, sys\n'
    'print("args=" + "|".join(sys.argv[1:]))\n'
    'print("env=" + os.environ.get("Q09", ""), file=sys.stderr)\n'
    'sys.exit(7)\n'
)
result = subprocess.run(
    ["/usr/bin/python3.12", "-S", "-c", child, "one", "two"],
    env=environment, capture_output=True, text=True, timeout=90,
)
assert result.returncode == 7, result.returncode
assert result.stdout == "args=one|two\n", result.stdout
assert result.stderr == "env=value\n", result.stderr

try:
    subprocess.run(
        ["/usr/bin/python3.12", "-S", "-c", "import time; time.sleep(30)"],
        env=environment, capture_output=True, timeout=1,
    )
except subprocess.TimeoutExpired:
    pass
else:
    raise AssertionError("timeout was not enforced")

process = subprocess.Popen(
    ["/usr/bin/python3.12", "-S", "-c", "import time; time.sleep(30)"],
    env=environment, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
)
os.kill(process.pid, signal.SIGTERM)
state = process.wait(timeout=10)
assert state == -signal.SIGTERM, state
print("Q09-OK")
`
	output, code := runPython312(t, connection, script, 300*time.Second)
	if code != 0 || output != "Q09-OK\n" {
		t.Fatalf("subprocess 用例 exit=%d output=%q", code, output)
	}
}
