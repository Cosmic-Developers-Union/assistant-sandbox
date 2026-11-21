package guest_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

const mmapRetentionDigest = "a85e6384b860910efa5c913d416f48661725166e8e8de0becdeedd0aac5478ca"

// TestFileMappingSurvivesDescriptorClose 验证 Linux mmap 语义:
// 文件映射在源描述符关闭后仍可读写。sem_open 在 mmap 后关闭
// /dev/shm 句柄,随后的 sem_wait/sem_post 必须继续有效。
func TestFileMappingSurvivesDescriptorClose(t *testing.T) {
	image, err := os.ReadFile("testdata/mmap-retention")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(image)
	if got := hex.EncodeToString(sum[:]); got != mmapRetentionDigest {
		t.Fatalf("mmap-retention 摘要=%s want %s", got, mmapRetentionDigest)
	}
	machine, err := instance.New(t.Context(), instance.Options{Deterministic: true, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = machine.Close(context.Background()) }()
	file, err := machine.Filesystem().Open(t.Context(), "/probe", vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(image); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	connection, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	output := &bytes.Buffer{}
	runCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	process, err := connection.Exec(runCtx, instance.ExecSpec{Program: "/probe", Streams: fd.Streams{Output: output, Error: output}})
	if err != nil {
		t.Fatal(err)
	}
	state, err := connection.Wait(runCtx, process.ID)
	if err != nil {
		t.Fatalf("文件映射探针: %v (output=%q)", err, output.String())
	}
	if state.ExitCode != 0 || output.String() != "MMAP-RETENTION-OK\n" {
		t.Fatalf("文件映射探针 exit=%d output=%q", state.ExitCode, output.String())
	}
}
