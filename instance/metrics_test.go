package instance

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
)

// TestOutputBudgetMetricsMatchQuota 验证 T03:客体写入按字节计入
// Output 配额,超限返回 EAGAIN,状态快照的已用量与扣减同源。
func TestOutputBudgetMetricsMatchQuota(t *testing.T) {
	image, err := os.ReadFile("../internal/guest/testdata/m-output")
	if err != nil {
		t.Fatal(err)
	}
	machine, err := New(t.Context(), Options{
		Deterministic: false,
		Seed:          1,
		Process:       resources.Limits{Memory: 64 << 20, Work: 1 << 28, Descriptors: 32, Output: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = machine.Close(context.Background()) }()
	probe, err := machine.Filesystem().Open(t.Context(), "/probe", vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := probe.Write(image); err != nil {
		t.Fatal(err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	connection, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	output := &bytes.Buffer{}
	input, inputWriter := io.Pipe()
	defer func() { _ = inputWriter.Close() }()
	process, err := connection.Exec(t.Context(), ExecSpec{Program: "/probe", Dir: "/work", Streams: fd.Streams{Input: input, Output: output}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, err := machine.Status(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if entry, ok := findProcess(status, process.ID); ok && entry.Budget.OutputUsed == 6 {
			if entry.Budget.OutputLimit != 8 {
				t.Fatalf("进程输出配额=%+v", entry.Budget)
			}
			if status.Session.OutputUsed < 6 {
				t.Fatalf("会话输出用量=%+v", status.Session)
			}
			if entry.Budget.WorkUsed == 0 || entry.Budget.MemoryUsed == 0 {
				t.Fatalf("CPU/内存指标未扣减: %+v", entry.Budget)
			}
			if len(status.Storage) == 0 {
				t.Fatalf("缺少磁盘指标: %+v", status)
			}
			if status.Network.Bytes != 0 {
				t.Fatalf("未使用网络但指标=%+v", status.Network)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("未观察到输出计费: %+v", status.Processes)
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := inputWriter.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	state, err := connection.Wait(t.Context(), process.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ExitCode != 0 || output.String() != "abcdef" {
		t.Fatalf("探针退出=%+v 输出=%q(断言码见 m-output.c)", state, output.String())
	}
}
