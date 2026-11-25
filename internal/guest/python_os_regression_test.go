package guest_test

import (
	"strings"
	"testing"
	"time"
)

// TestPython312OSRegressions 锁定 test_os 中曾阻塞或失败的四条
// 上游用例:memfd_create、坏描述符 fpathconf、asyncio sendfile
// 收尾与 fork 线程告警。
func TestPython312OSRegressions(t *testing.T) {
	machine, connection := python312Guest(t)
	extractPython312Test(t, machine, python312Asset(t,
		"testdata/python312/python312-test.tar.gz", python312TestDigest))
	output, diagnostic, code := runGuestProgram(t, connection,
		"/usr/bin/python3.12", []string{"-S", "-m", "unittest", "-v",
			"test.test_os.MemfdCreateTests.test_memfd_create",
			"test.test_os.TestInvalidFD.test_fpathconf",
			"test.test_os.TestSendfile.test_send_whole_file",
			"test.test_os.ForkTests.test_fork_warns_when_non_python_thread_exists",
		},
		map[string]string{
			"HOME": "/work", "PATH": "/usr/bin:/bin", "LANG": "C.UTF-8",
			"PYTHONHOME": "/usr", "PYTHONIOENCODING": "utf-8",
		}, 5*time.Minute)
	if code != 0 || !strings.Contains(diagnostic, "Ran 4 tests") ||
		!strings.Contains(diagnostic, "\nOK") {
		t.Fatalf("CPython test_os 回归 exit=%d\nstdout:\n%s\nstderr:\n%s",
			code, output, diagnostic)
	}
}

// TestPython312AsyncioThreadsafeWake 验证 call_soon_threadsafe 经
// AF_UNIX 自管道唤醒 selector,且 asyncio.Runner 能正常关闭默认
// 线程池。旧实现把 musl send(2) 的 sendto 路径报 ENOTSOCK,并在
// 有限超时中睡满切片后才复查就绪,导致关闭挂起。
func TestPython312AsyncioThreadsafeWake(t *testing.T) {
	machine, connection := python312Guest(t)
	_ = machine
	script := `import asyncio, os, threading, time
async def main():
    loop = asyncio.get_running_loop()
    print("to_thread", await asyncio.to_thread(os.getpid), flush=True)
    for index in range(3):
        future = loop.create_future()
        def wake(i=index, f=future):
            time.sleep(0.05)
            loop.call_soon_threadsafe(f.set_result, i)
        threading.Thread(target=wake).start()
        print("wake", await future, flush=True)
asyncio.run(main())
print("done", flush=True)
`
	output, code := runPython312(t, connection, script, 2*time.Minute)
	if code != 0 || !strings.HasPrefix(output, "to_thread ") ||
		!strings.HasSuffix(output, "done\n") {
		t.Fatalf("asyncio 线程唤醒 exit=%d output=%q", code, output)
	}
}
