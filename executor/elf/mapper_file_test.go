package elf

import (
	"context"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/abi/linux/common"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
)

// mappedFile 是 mapper 文件映射测试用的常规文件句柄。
type mappedFile struct {
	data     []byte
	readable bool
	writable bool
}

func (f *mappedFile) Read(p []byte) (int, error)  { return copy(p, f.data), nil }
func (f *mappedFile) Write(p []byte) (int, error) { f.data = append(f.data, p...); return len(p), nil }
func (f *mappedFile) Close() error                { return nil }

func (f *mappedFile) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(f.data)) {
		return 0, nil
	}
	return copy(p, f.data[off:]), nil
}

func (f *mappedFile) WriteAt(p []byte, off int64) (int, error) {
	if end := off + int64(len(p)); end > int64(len(f.data)) {
		grown := make([]byte, end)
		copy(grown, f.data)
		f.data = grown
	}
	return copy(f.data[off:], p), nil
}

func (f *mappedFile) Readable() bool { return f.readable }
func (f *mappedFile) Writable() bool { return f.writable }

// TestMapperFilePrivateSnapshot 验证 E08:私有文件映射以文件内容为
// 初始快照,客体读取命中,修改不写回源文件,快照独立于文件后续变化。
func TestMapperFilePrivateSnapshot(t *testing.T) {
	ctx := context.Background()
	space := memory.New(0)
	m := &mapper{table: space, ceiling: 1 << 40}
	file := &mappedFile{data: []byte("private-bytes"), readable: true, writable: true}

	start, err := m.File(ctx, common.FileMapRequest{
		MapRequest: common.MapRequest{
			Length: 2 * memory.PageSize, Prot: common.ProtRead | common.ProtWrite,
		},
		File: file,
	})
	if err != nil {
		t.Fatal(err)
	}
	if start != mmapFloor {
		t.Fatalf("首个文件映射应从 floor 开始: %#x", start)
	}
	buffer := make([]byte, 13)
	if err := space.Read(ctx, start, buffer); err != nil || string(buffer) != "private-bytes" {
		t.Fatalf("快照读取=%q err=%v", buffer, err)
	}
	// 修改私有映射:不写回源文件。
	if err := space.Write(ctx, start, []byte("PRIVATE")); err != nil {
		t.Fatal(err)
	}
	if string(file.data) != "private-bytes" {
		t.Fatalf("私有修改写回源文件: %q", file.data)
	}
	modified := make([]byte, 13)
	if err := space.Read(ctx, start, modified); err != nil || string(modified) != "PRIVATE-bytes" {
		t.Fatalf("私有修改可见性=%q err=%v", modified, err)
	}
	// 带偏移的快照:偏移 8 起的内容。
	at, err := m.File(ctx, common.FileMapRequest{
		MapRequest: common.MapRequest{Length: memory.PageSize, Prot: common.ProtRead, Hint: mmapFloor + 16*memory.PageSize},
		File:       file, Offset: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if at != mmapFloor+16*memory.PageSize {
		t.Fatalf("提示选址: %#x", at)
	}
	offsetView := make([]byte, 5)
	if err := space.Read(ctx, at, offsetView); err != nil || string(offsetView) != "bytes" {
		t.Fatalf("偏移快照=%q err=%v", offsetView, err)
	}
}

// TestMapperFileSharedPassthrough 验证 E09:共享文件映射写入直通并
// 即时持久,同一文件的两个映射(不同偏移)彼此一致,msync 校验
// 覆盖性;越过文件尾的读取为零页。
func TestMapperFileSharedPassthrough(t *testing.T) {
	ctx := context.Background()
	space := memory.New(0)
	m := &mapper{table: space, ceiling: 1 << 40}
	file := &mappedFile{data: make([]byte, 32), readable: true, writable: true}
	copy(file.data, "0123456789ABCDEF")

	first, err := m.File(ctx, common.FileMapRequest{
		MapRequest: common.MapRequest{Length: 2 * memory.PageSize, Prot: common.ProtRead | common.ProtWrite},
		File:       file, Shared: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 写入直通:文件数据即时变化。
	if err := space.Write(ctx, first+4, []byte("shared!")); err != nil {
		t.Fatal(err)
	}
	if got := string(file.data[4:11]); got != "shared!" {
		t.Fatalf("直通写=%q", got)
	}
	// 第二个映射(同文件偏移 0):读取见到第一个映射的写入。
	second, err := m.File(ctx, common.FileMapRequest{
		MapRequest: common.MapRequest{Length: memory.PageSize, Prot: common.ProtRead | common.ProtWrite, Hint: mmapFloor + 16*memory.PageSize},
		File:       file, Shared: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	peer := make([]byte, 11)
	if err := space.Read(ctx, second, peer); err != nil || string(peer) != "0123shared!" {
		t.Fatalf("第二映射=%q err=%v", peer, err)
	}
	// 经第二映射写入,第一映射可见(双向一致)。
	if err := space.Write(ctx, second, []byte("X")); err != nil {
		t.Fatal(err)
	}
	one := make([]byte, 1)
	if err := space.Read(ctx, first, one); err != nil || one[0] != 'X' {
		t.Fatalf("双向一致=%q", one)
	}
	// 越文件尾读取零填充;msync 完整范围成功、空洞 ENOMEM。
	tail := make([]byte, 8)
	if err := space.Read(ctx, first+memory.PageSize+4080, tail); err != nil {
		t.Fatal(err)
	}
	for _, b := range tail {
		if b != 0 {
			t.Fatalf("越尾非零: %v", tail)
		}
	}
	if err := m.Sync(ctx, first, first+2*memory.PageSize); err != nil {
		t.Fatal(err)
	}
	if err := m.Sync(ctx, first, first+3*memory.PageSize); err == nil {
		t.Fatal("含空洞的 Sync 应报 ENOMEM")
	}
}
