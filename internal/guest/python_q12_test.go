package guest_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
)

const (
	python312PipDigest    = "003e001ea422c6705f0355efb17dc7bda3402e056dbc29ddf35d2a76683ec735"
	python312PureWheel    = "880d513ddcb321c6dba92277b69cc3f3d5b50e10abf3a2daadd3e524e15847ed"
	python312NativeWheel  = "2a01add49fbe7eca2d5baaa222e6bf87aeee02ff836e48495a91c94165b8dfe9"
	python312VenvSitePack = "/work/q12venv/lib/python3.12/site-packages"
)

// TestPython312VenvOfflineInstall 验证 Q12:离线创建 venv,安装固定
// 纯 Python 包与本项目构建的 riscv64 原生 wheel,重连后仍可使用。
func TestPython312VenvOfflineInstall(t *testing.T) {
	machine, connection := python312Guest(t)
	create := `import subprocess, sys
env = {"PATH": "/usr/bin:/bin", "HOME": "/work", "LANG": "C.UTF-8", "PYTHONHOME": "/usr"}
result = subprocess.run([sys.executable, "-m", "venv", "--without-pip", "/work/q12venv"], env=env, capture_output=True, text=True, timeout=300)
assert result.returncode == 0, (result.returncode, result.stderr[-1000:])
print("Q12-VENV")
`
	output, code := runPython312(t, connection, create, 600*time.Second)
	if code != 0 || !strings.Contains(output, "Q12-VENV") {
		t.Fatalf("创建 venv exit=%d output=%q", code, output)
	}

	extractPython312Pip(t, machine, python312Asset(t, "testdata/python312/python312-pip.tar.gz", python312PipDigest))
	writeGuestFile(t, machine, "/work/wheels/q12_pure-1.0-py3-none-any.whl",
		python312Asset(t, "testdata/python312/q12_pure-1.0-py3-none-any.whl", python312PureWheel), 0o644)
	writeGuestFile(t, machine, "/work/wheels/q06_native-1.0-cp312-cp312-linux_riscv64.whl",
		python312Asset(t, "testdata/python312/q06_native-1.0-cp312-cp312-linux_riscv64.whl", python312NativeWheel), 0o644)

	install := `import subprocess
env = {"PATH": "/usr/bin:/bin", "HOME": "/work", "LANG": "C.UTF-8", "PYTHONDONTWRITEBYTECODE": "1"}
result = subprocess.run(["/work/q12venv/bin/python", "-m", "pip", "install", "--no-index", "--find-links", "/work/wheels", "--no-compile", "q12_pure", "q06_native"], env=env, capture_output=True, text=True, timeout=1800)
assert result.returncode == 0, (result.returncode, result.stdout[-2000:], result.stderr[-2000:])
check = subprocess.run(["/work/q12venv/bin/python", "-c", "import q06_native, q12_pure; assert q12_pure.answer() == 42; assert q06_native.add(20, 22) == 42; print('Q12-OK')"], env=env, capture_output=True, text=True, timeout=300)
assert check.returncode == 0, (check.returncode, check.stderr[-1000:])
print(check.stdout, end="")
`
	output, code = runPython312(t, connection, install, 2100*time.Second)
	if code != 0 || !strings.HasSuffix(output, "Q12-OK\n") {
		t.Fatalf("离线安装 exit=%d output=%q", code, output)
	}

	// 退出安装进程与连接后,在同一 VM 重新连接,venv 与已安装包保留。
	if err := connection.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	reconnected, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	verify := `import subprocess
env = {"PATH": "/usr/bin:/bin", "HOME": "/work", "LANG": "C.UTF-8", "PYTHONDONTWRITEBYTECODE": "1"}
result = subprocess.run(["/work/q12venv/bin/python", "-c", "import q06_native, q12_pure; print(q12_pure.answer() + q06_native.add(20, 22))"], env=env, capture_output=True, text=True, timeout=300)
print("reconnect", result.returncode, repr(result.stdout), repr(result.stderr[-500:]))
`
	output, code = runPython312(t, reconnected, verify, 600*time.Second)
	if code != 0 || !strings.Contains(output, "reconnect 0 '84\\n'") {
		t.Fatalf("重连后 venv exit=%d output=%q", code, output)
	}
}

// extractPython312Pip 把预编译 pip 归档展开到 venv site-packages。
func extractPython312Pip(t *testing.T, machine *instance.VM, archive []byte) {
	t.Helper()
	extractPython312PipTo(t, machine, archive, python312VenvSitePack)
}

// extractPython312PipTo 把预编译 pip 归档展开到指定 site-packages。
func extractPython312PipTo(t *testing.T, machine *instance.VM, archive []byte, destination string) {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	files := tar.NewReader(reader)
	for {
		header, err := files.Next()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		name := path.Clean(strings.TrimPrefix(header.Name, "./"))
		target := destination + "/" + name
		switch header.Typeflag {
		case tar.TypeDir:
			_ = machine.Filesystem().Mkdir(t.Context(), target, 0o755)
		case tar.TypeReg:
			data, err := io.ReadAll(io.LimitReader(files, 16<<20))
			if err != nil {
				t.Fatal(err)
			}
			mode := header.FileInfo().Mode().Perm()
			if mode == 0 {
				mode = 0o644
			}
			writeGuestFile(t, machine, target, data, mode)
		}
	}
}
