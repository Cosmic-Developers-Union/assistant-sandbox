package guest_test

import (
	"testing"
	"time"
)

// TestPython312Threading 验证 Q08:线程、互斥锁、条件变量与
// thread-local 在客体完成有界并发用例。
func TestPython312Threading(t *testing.T) {
	_, connection := python312Guest(t)
	script := `import threading, time

counter = 0
lock = threading.Lock()

def worker(rounds):
    global counter
    for _ in range(rounds):
        with lock:
            counter += 1

threads = [threading.Thread(target=worker, args=(500,)) for _ in range(4)]
for thread in threads:
    thread.start()
for thread in threads:
    thread.join()
assert counter == 2000, counter

queue = []
condition = threading.Condition()
consumed = []

def consumer():
    with condition:
        deadline = time.monotonic() + 5.0
        while not queue:
            remaining = deadline - time.monotonic()
            assert remaining > 0, "condition timeout"
            condition.wait(remaining)
        consumed.append(queue.pop(0))

thread = threading.Thread(target=consumer)
thread.start()
with condition:
    queue.append(42)
    condition.notify()
thread.join()
assert consumed == [42], consumed

local = threading.local()
seen = {}
barrier = threading.Barrier(2)

def tls_worker(value):
    local.value = value
    barrier.wait()
    seen[value] = local.value

first = threading.Thread(target=tls_worker, args=("alpha",))
second = threading.Thread(target=tls_worker, args=("beta",))
first.start()
second.start()
first.join()
second.join()
assert seen == {"alpha": "alpha", "beta": "beta"}, seen

event = threading.Event()
assert not event.wait(0.01)
event.set()
assert event.wait(1.0)
print("Q08-OK")
`
	output, code := runPython312(t, connection, script, 240*time.Second)
	if code != 0 || output != "Q08-OK\n" {
		t.Fatalf("线程用例 exit=%d output=%q", code, output)
	}
}
