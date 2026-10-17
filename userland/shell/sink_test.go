package shell

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

func TestMutableSinkForwardsAndSwallows(t *testing.T) {
	sink := &mutableSink{}

	if n, err := sink.Write([]byte("dropped")); err != nil || n != 7 {
		t.Fatalf("无目标应丢弃并成功: n=%d err=%v", n, err)
	}
	var buffer bytes.Buffer
	sink.setTarget(&buffer)
	if n, err := sink.Write([]byte("kept")); err != nil || n != 4 {
		t.Fatalf("有目标应转发: n=%d err=%v", n, err)
	}
	if buffer.String() != "kept" {
		t.Fatalf("buffer=%q", buffer.String())
	}

	snapshot := sink.bytes()
	buffer.WriteString("more")
	if string(snapshot) != "kept" {
		t.Fatalf("bytes 应为副本: %q", snapshot)
	}

	sink.setTarget(&discardWriter{})
	if sink.bytes() != nil {
		t.Fatal("非缓冲目标应返回 nil")
	}

	sink.setTarget(nil)
	if _, err := sink.Write([]byte("x")); err != nil {
		t.Fatalf("重新置空后应丢弃: %v", err)
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestEnvironRoundTrip(t *testing.T) {
	environ := []string{"B=2", "A=1", "WITH_EQUALS=a=b", "NOEQUALS"}
	asMap := environMap(environ)
	if asMap["A"] != "1" || asMap["B"] != "2" {
		t.Fatalf("map=%v", asMap)
	}
	if asMap["WITH_EQUALS"] != "a=b" {
		t.Fatalf("应按第一个 = 切分: %v", asMap)
	}
	if _, exists := asMap["NOEQUALS"]; exists {
		t.Fatalf("缺少 = 的条目不应进入映射: %v", asMap)
	}
	back := environList(asMap)
	if !sort.StringsAreSorted(back) {
		t.Fatalf("列表应按名排序: %v", back)
	}
	want := []string{"A=1", "B=2", "WITH_EQUALS=a=b"}
	if strings.Join(back, ",") != strings.Join(want, ",") {
		t.Fatalf("back=%v want %v", back, want)
	}

	if len(environList(nil)) != 0 {
		t.Fatal("空映射应产生空列表")
	}
}

func TestSlotExitStatusAndBusy(t *testing.T) {
	ctx := context.Background()

	slot := newTestSlot(t, t.TempDir())
	output, err := slot.Step(ctx, []byte("exit 7"))
	if err != nil {
		t.Fatal(err)
	}
	if output.Status != 7 {
		t.Fatalf("status=%d want 7", output.Status)
	}

	slot2 := newTestSlot(t, t.TempDir())
	output, err = slot2.Step(ctx, []byte("false"))
	if err != nil {
		t.Fatal(err)
	}
	if output.Status != 1 {
		t.Fatalf("false 的 status=%d want 1", output.Status)
	}

	slot3 := newTestSlot(t, t.TempDir())
	slot3.mu.Lock()
	slot3.session = nil
	slot3.mu.Unlock()
	if _, err := slot3.Step(ctx, []byte("echo x")); !errors.Is(err, errno.Busy) {
		t.Fatalf("已结束槽位应报 EBUSY: %v", err)
	}
}

func TestNewSlotValidatesInputs(t *testing.T) {
	if _, err := NewSlot(nil, proc.Proc{}, t.TempDir(), nil); !errors.Is(err, errno.Invalid) {
		t.Fatalf("缺少内核应报 EINVAL: %v", err)
	}
	kernel, parent, err := newKernel(t.TempDir(), nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	slot, err := NewSlot(kernel, parent, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("正常进程应可建槽: %v", err)
	}
	if err := slot.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
