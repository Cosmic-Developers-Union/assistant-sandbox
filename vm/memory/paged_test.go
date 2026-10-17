package memory

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

var _ interface {
	Read(ctx context.Context, address uint64, buffer []byte) error
	Write(ctx context.Context, address uint64, buffer []byte) error
} = (*Table)(nil)

func TestReadWriteRoundTrip(t *testing.T) {
	ctx := context.Background()
	table := New(0)
	if err := table.Map(ctx, Region{Start: page(2), End: page(4), Prot: ProtRead | ProtWrite, Name: "data"}); err != nil {
		t.Fatal(err)
	}
	payload := []byte("hello, paged memory")
	if err := table.Write(ctx, page(2)+3, payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if err := table.Read(ctx, page(2)+3, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("往返结果=%q want %q", got, payload)
	}

	crossing := []byte{0xA, 0xB, 0xC, 0xD, 0xE, 0xF}
	if err := table.Write(ctx, page(3)-3, crossing); err != nil {
		t.Fatal(err)
	}
	back := make([]byte, len(crossing))
	if err := table.Read(ctx, page(3)-3, back); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back, crossing) {
		t.Fatalf("跨页往返=%x want %x", back, crossing)
	}
}

func TestSparsePagesReadAsZero(t *testing.T) {
	ctx := context.Background()
	table := New(0)
	if err := table.Map(ctx, Region{Start: page(5), End: page(6), Prot: ProtRead | ProtWrite}); err != nil {
		t.Fatal(err)
	}
	buffer := bytes.Repeat([]byte{0xFF}, 16)
	if err := table.Read(ctx, page(5)+100, buffer); err != nil {
		t.Fatal(err)
	}
	for index, value := range buffer {
		if value != 0 {
			t.Fatalf("第 %d 字节=%#x want 0", index, value)
		}
	}

	if err := table.Write(ctx, page(5), []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := table.Read(ctx, page(5)+PageSize-1, buffer[:1]); err != nil {
		t.Fatal(err)
	}
	if buffer[0] != 0 {
		t.Fatalf("未写页=%#x want 0", buffer[0])
	}
}

func TestWriteRejectsBeforeSideEffect(t *testing.T) {
	ctx := context.Background()
	table := New(0)
	if err := table.Map(ctx, Region{Start: page(7), End: page(8), Prot: ProtRead | ProtWrite}); err != nil {
		t.Fatal(err)
	}

	if err := table.Write(ctx, page(8)-2, []byte{9, 9, 9, 9}); !errors.Is(err, errno.Range) {
		t.Fatalf("越界写入应报 ERANGE: %v", err)
	}
	buffer := bytes.Repeat([]byte{0xFF}, 4)
	if err := table.Read(ctx, page(8)-4, buffer); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buffer, []byte{0, 0, 0, 0}) {
		t.Fatalf("被拒绝的写入留下了痕迹: %x", buffer)
	}
}

func TestReadWriteFaults(t *testing.T) {
	ctx := context.Background()
	table := New(0)
	if err := table.Map(ctx, Region{Start: page(9), End: page(10), Prot: ProtRead}); err != nil {
		t.Fatal(err)
	}
	if err := table.Map(ctx, Region{Start: page(10), End: page(11), Prot: ProtRead | ProtWrite}); err != nil {
		t.Fatal(err)
	}
	if err := table.Read(ctx, page(20), make([]byte, 8)); !errors.Is(err, errno.Range) {
		t.Errorf("未映射读取=%v want ERANGE", err)
	}
	if err := table.Write(ctx, page(9), []byte{1}); !errors.Is(err, errno.Permission) {
		t.Errorf("写只读页=%v want EACCES", err)
	}
	if err := table.Read(ctx, page(10)-4, make([]byte, 8)); !errors.Is(err, errno.Range) {
		t.Errorf("跨映射边界=%v want ERANGE", err)
	}
	if err := table.Read(ctx, ^uint64(0)-3, make([]byte, 8)); !errors.Is(err, errno.Invalid) {
		t.Errorf("区间溢出=%v want EINVAL", err)
	}

	if err := table.Read(ctx, page(20), nil); err != nil {
		t.Errorf("零长度读取=%v want nil", err)
	}
	if err := table.Write(ctx, page(20), nil); err != nil {
		t.Errorf("零长度写入=%v want nil", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := table.Read(cancelled, page(10), make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Errorf("取消读取=%v want context.Canceled", err)
	}
	if err := table.Write(cancelled, page(10), []byte{1}); !errors.Is(err, context.Canceled) {
		t.Errorf("取消写入=%v want context.Canceled", err)
	}
}

func TestUnmapDropsPages(t *testing.T) {
	ctx := context.Background()
	table := New(0)
	region := Region{Start: page(12), End: page(13), Prot: ProtRead | ProtWrite}
	if err := table.Map(ctx, region); err != nil {
		t.Fatal(err)
	}
	if err := table.Write(ctx, page(12), []byte{7, 7, 7, 7}); err != nil {
		t.Fatal(err)
	}
	if err := table.Unmap(ctx, region.Start, region.End); err != nil {
		t.Fatal(err)
	}
	if err := table.Map(ctx, region); err != nil {
		t.Fatal(err)
	}
	buffer := bytes.Repeat([]byte{0xFF}, 4)
	if err := table.Read(ctx, page(12), buffer); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buffer, []byte{0, 0, 0, 0}) {
		t.Fatalf("重新映射后读到了旧数据: %x", buffer)
	}
}

func TestCheckAndAccessAgree(t *testing.T) {
	ctx := context.Background()
	table := New(0)
	if err := table.Map(ctx, Region{Start: page(14), End: page(15), Prot: ProtRead}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		label   string
		address uint64
		length  uint64
		prot    Prot
		want    error
	}{
		{"未映射读取", page(30), 8, ProtRead, errno.Range},
		{"只读页写入", page(14), 8, ProtWrite, errno.Permission},
		{"跨映射边界", page(15) - 4, 8, ProtRead, errno.Range},
	} {
		if err := table.Check(ctx, tc.address, tc.length, tc.prot); !errors.Is(err, tc.want) {
			t.Errorf("%s: Check=%v want %v", tc.label, err, tc.want)
		}
		var err error
		if tc.prot.CanWrite() {
			err = table.Write(ctx, tc.address, make([]byte, tc.length))
		} else {
			err = table.Read(ctx, tc.address, make([]byte, tc.length))
		}
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: 访问=%v want %v", tc.label, err, tc.want)
		}
	}
}
