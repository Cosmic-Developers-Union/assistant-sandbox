package fd

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// passedStream 是记录关闭次数与读写偏移的流测试替身。
type passedStream struct {
	offset int64
	data   []byte
	closes int
}

func (s *passedStream) Read(p []byte) (int, error) {
	if s.offset >= int64(len(s.data)) {
		return 0, io.EOF
	}
	n := copy(p, s.data[s.offset:])
	s.offset += int64(n)
	return n, nil
}

func (s *passedStream) Write(p []byte) (int, error) {
	s.data = append(s.data[:s.offset], p...)
	s.offset += int64(len(p))
	return len(p), nil
}

func (s *passedStream) Close() error {
	s.closes++
	return nil
}

func (s *passedStream) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		s.offset = offset
	case io.SeekCurrent:
		s.offset += offset
	case io.SeekEnd:
		s.offset = int64(len(s.data)) + offset
	}
	return s.offset, nil
}

// TestPassedRightsShareOpenFileDescription 验证 M08 的所有权:发送侧
// Retain 后关闭自己的描述符不影响接收方;两侧共享同一打开文件描述
// (偏移一致),引用恰好关闭一次。
func TestPassedRightsShareOpenFileDescription(t *testing.T) {
	ctx := context.Background()
	table := New()
	table.Attach(1, Streams{})
	table.Attach(2, Streams{})

	handle := &passedStream{}
	if err := table.Install(1, 3, handle); err != nil {
		t.Fatal(err)
	}
	passed, err := table.Retain(1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := table.Close(ctx, 1, 3); err != nil {
		t.Fatal(err)
	}
	if handle.closes != 0 {
		t.Fatalf("发送侧关闭后引用仍存活,close=%d", handle.closes)
	}
	id, err := table.InstallPassed(2, passed)
	if err != nil {
		t.Fatal(err)
	}
	if id != 3 {
		t.Fatalf("接收描述符=%d want 3", id)
	}
	// 同一传递引用不能安装两次。
	if _, err := table.InstallPassed(2, passed); !errors.Is(err, errno.Invalid) {
		t.Fatalf("重复安装=%v want EINVAL", err)
	}
	// 共享打开文件描述:经发送侧写入的偏移对接收侧可见。
	stream, err := table.Get(ctx, 2, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	reader, err := table.Get(ctx, 2, id)
	if err != nil {
		t.Fatal(err)
	}
	seeker, ok := reader.(io.Seeker)
	if !ok {
		t.Fatal("接收句柄应支持定位")
	}
	position, err := seeker.Seek(0, io.SeekCurrent)
	if err != nil || position != 3 {
		t.Fatalf("共享偏移=%d err=%v want 3", position, err)
	}
	if err := table.Close(ctx, 2, id); err != nil {
		t.Fatal(err)
	}
	if handle.closes != 1 {
		t.Fatalf("最终关闭次数=%d want 1", handle.closes)
	}
}

// TestPassedRightsReleaseOnDrop 验证传递失败/丢弃路径:未安装的引用
// 经 Release 归还,重复归幂等;发送侧仍持有的引用不受影响。
func TestPassedRightsReleaseOnDrop(t *testing.T) {
	table := New()
	table.Attach(1, Streams{})
	handle := &passedStream{}
	if err := table.Install(1, 4, handle); err != nil {
		t.Fatal(err)
	}
	passed, err := table.Retain(1, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := passed.Release(); err != nil {
		t.Fatal(err)
	}
	if err := passed.Release(); err != nil {
		t.Fatalf("重复归还=%v", err)
	}
	if handle.closes != 0 {
		t.Fatalf("发送侧仍持有引用,close=%d", handle.closes)
	}
	if err := table.Detach(1); err != nil {
		t.Fatal(err)
	}
	if handle.closes != 1 {
		t.Fatalf("最终关闭次数=%d want 1", handle.closes)
	}
	// 清理后的传递引用不能再安装。
	table.Attach(2, Streams{})
	if _, err := table.InstallPassed(2, passed); !errors.Is(err, errno.Invalid) {
		t.Fatalf("已归还引用安装=%v want EINVAL", err)
	}
}
