package sumcmd

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"
)

type failReader struct{}

func (failReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestDirectHelpers(t *testing.T) {
	rc := &toolcompat.RunContext{Stdio: toolcompat.Stdio{Out: io.Discard, Err: io.Discard}}
	result, err := sumOperand(rc, "-", false)
	if err != nil || result.checksum != 0 || result.blocks != 0 {
		t.Fatalf("nil stdin 得到 %+v err=%v", result, err)
	}
	if _, err := bsdSum(failReader{}); err == nil {
		t.Fatal("bsdSum 读失败应报错")
	}
	if _, err := sysvSum(failReader{}); err == nil {
		t.Fatal("sysvSum 读失败应报错")
	}
	if got := blocks(0, 1024); got != 0 {
		t.Fatalf("空输入块数应为 0: %d", got)
	}
	if got := blocks(1025, 1024); got != 2 {
		t.Fatalf("跨块应向上取整: %d", got)
	}
	if got := errMsg(errIsDirectory); got != "Is a directory" {
		t.Fatalf("目录文案: %q", got)
	}
	if got := errMsg(vfs.ErrNotExist); got != "No such file or directory" {
		t.Fatalf("缺失文案: %q", got)
	}
	if got := errMsg(vfs.ErrPermission); got != "Permission denied" {
		t.Fatalf("权限文案: %q", got)
	}
	other := errors.New("其他")
	if got := errMsg(other); got != "其他" {
		t.Fatalf("兜底文案: %q", got)
	}
	if _, err := bsdSum(strings.NewReader("")); err != nil {
		t.Fatalf("空输入应成功: %v", err)
	}
}
