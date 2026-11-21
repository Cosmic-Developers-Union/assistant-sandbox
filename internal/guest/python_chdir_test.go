package guest_test

import (
	"testing"
	"time"
)

// TestPython312WorkingDirectoryAndPermissions 验证 chdir/fchdir 与
// fchmod/fchmodat 的客体会话语义:相对路径解析、父目录、权限位。
func TestPython312WorkingDirectoryAndPermissions(t *testing.T) {
	_, connection := python312Guest(t)
	script := `import os, stat
os.makedirs("/work/chdir", exist_ok=True)
os.chdir("/work/chdir")
assert os.getcwd() == "/work/chdir", os.getcwd()
with open("relative.txt", "w") as file:
    file.write("data")
assert os.path.exists("/work/chdir/relative.txt")
os.chdir("..")
assert os.getcwd() == "/work", os.getcwd()

path = "/work/chdir/mode.txt"
with open(path, "w") as file:
    file.write("mode")
os.chmod(path, 0o600)
assert stat.S_IMODE(os.stat(path).st_mode) == 0o600
fd = os.open(path, os.O_RDWR)
try:
    os.fchmod(fd, 0o640)
finally:
    os.close(fd)
assert stat.S_IMODE(os.stat(path).st_mode) == 0o640
print("CHDIR-CHMOD-OK")
`
	output, code := runPython312(t, connection, script, 120*time.Second)
	if code != 0 || output != "CHDIR-CHMOD-OK\n" {
		t.Fatalf("chdir/chmod exit=%d output=%q", code, output)
	}
}
