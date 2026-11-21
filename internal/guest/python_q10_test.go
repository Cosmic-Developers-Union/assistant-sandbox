package guest_test

import (
	"strings"
	"testing"
	"time"
)

// TestPython312Multiprocessing 验证 Q10:Linux 默认 fork 与 spawn
// 主程序方式下的进程、队列和共享数据。
func TestPython312Multiprocessing(t *testing.T) {
	machine, connection := python312Guest(t)
	fork := `import multiprocessing as mp
ctx = mp.get_context("fork")
def worker(queue, value, lock):
    with lock:
        value.value += 1
    queue.put("fork")
if __name__ == "__main__":
    queue = ctx.Queue()
    value = ctx.Value("i", 0)
    lock = ctx.Lock()
    process = ctx.Process(target=worker, args=(queue, value, lock))
    process.start()
    process.join(60)
    assert process.exitcode == 0, process.exitcode
    assert queue.get(timeout=10) == "fork"
    assert value.value == 1
    print("Q10-FORK-OK")
`
	output, code := runPython312(t, connection, fork, 300*time.Second)
	if code != 0 || !strings.HasSuffix(output, "Q10-FORK-OK\n") {
		t.Fatalf("fork exit=%d output=%q", code, output)
	}

	module := `import multiprocessing as mp


def worker(queue, value, lock):
    with lock:
        value.value += 5
    queue.put("spawn")


def main():
    ctx = mp.get_context("spawn")
    queue = ctx.Queue()
    value = ctx.Value("i", 0)
    lock = ctx.Lock()
    process = ctx.Process(target=worker, args=(queue, value, lock))
    process.start()
    process.join(120)
    assert process.exitcode == 0, process.exitcode
    assert queue.get(timeout=10) == "spawn"
    assert value.value == 5
    print("Q10-SPAWN-OK")


if __name__ == "__main__":
    main()
`
	writeGuestFile(t, machine, "/work/q10_spawn_mod.py", []byte(module), 0o644)
	output, code = runPython312Module(t, connection, "q10_spawn_mod", 480*time.Second)
	if code != 0 || !strings.HasSuffix(output, "Q10-SPAWN-OK\n") {
		t.Fatalf("spawn exit=%d output=%q", code, output)
	}
}
