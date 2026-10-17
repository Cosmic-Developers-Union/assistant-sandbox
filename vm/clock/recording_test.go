package clock

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/replay"
)

type stepClock struct {
	value uint64
	step  uint64
	sleep uint64
	fail  error
}

func (c *stepClock) Monotonic(context.Context) (uint64, error) {
	if c.fail != nil {
		return 0, c.fail
	}
	c.value += c.step
	return c.value, nil
}

func (c *stepClock) Wall(ctx context.Context) (int64, error) {
	value, err := c.Monotonic(ctx)
	if err != nil {
		return 0, err
	}
	if value > 1<<63-1 {
		return 0, errno.Overflow
	}
	return int64(value), nil
}

func (c *stepClock) Sleep(_ context.Context, nanoseconds uint64) error {
	if c.fail != nil {
		return c.fail
	}
	c.sleep += nanoseconds
	c.value += nanoseconds
	return nil
}

func TestRecordingReplaysClockReads(t *testing.T) {
	ctx := context.Background()
	journal := replay.Record()
	live := &stepClock{step: 100}
	recorder := NewRecording(live, journal, 1)
	first, err := recorder.Monotonic(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := recorder.Monotonic(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first != 100 || second != 200 {
		t.Fatalf("记录值=%d,%d", first, second)
	}
	facts := journal.Facts()
	if len(facts) != 2 || facts[0].Operation != replay.OpClockRead {
		t.Fatalf("应记录两条时钟事实: %+v", facts)
	}

	replayer := NewRecording(&stepClock{step: 100}, replay.Replay(facts), 1)
	replayed, err := replayer.Monotonic(ctx)
	if err != nil || replayed != 100 {
		t.Fatalf("回放首个值=%d err=%v", replayed, err)
	}
	replayed, err = replayer.Monotonic(ctx)
	if err != nil || replayed != 200 {
		t.Fatalf("回放第二个值=%d err=%v", replayed, err)
	}
}

func TestReplayDetectsClockDrift(t *testing.T) {
	ctx := context.Background()
	journal := replay.Record()
	recorder := NewRecording(&stepClock{step: 100}, journal, 1)
	if _, err := recorder.Monotonic(ctx); err != nil {
		t.Fatal(err)
	}
	facts := journal.Facts()

	drifted := NewRecording(&stepClock{step: 1}, replay.Replay(facts), 1)
	_, err := drifted.Monotonic(ctx)
	if !errors.Is(err, errno.Invalid) {
		t.Fatalf("时钟漂移应报分歧: %v", err)
	}
	var mismatch *replay.MismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("应报告 MismatchError: %v", err)
	}
	if mismatch.Reason == "" {
		t.Fatalf("分歧应带原因: %+v", mismatch)
	}
}

func TestReplaySleepDoesNotAdvanceTime(t *testing.T) {
	ctx := context.Background()
	journal := replay.Record()
	live := &Virtual{}
	recorder := NewRecording(live, journal, 1)
	if err := recorder.Sleep(ctx, 500); err != nil {
		t.Fatal(err)
	}
	reading, err := recorder.Monotonic(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reading != 500 {
		t.Fatalf("等待后读取=%d want 500", reading)
	}

	fresh := &Virtual{}
	replayer := NewRecording(fresh, replay.Replay(journal.Facts()), 1)
	if err := replayer.Sleep(ctx, 500); err != nil {
		t.Fatalf("回放等待应成功: %v", err)
	}
	if elapsed := elapsedOf(t, fresh); elapsed != 500 {
		t.Fatalf("回放应把时钟推进到记录终点: %d", elapsed)
	}
	replayed, err := replayer.Monotonic(ctx)
	if err != nil || replayed != 500 {
		t.Fatalf("回放读取=%d err=%v", replayed, err)
	}
}

func elapsedOf(t *testing.T, virtual *Virtual) uint64 {
	t.Helper()
	value, err := virtual.Monotonic(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestWallReadIsRecorded(t *testing.T) {
	ctx := context.Background()
	journal := replay.Record()
	recorder := NewRecording(&stepClock{step: 7}, journal, 1)
	value, err := recorder.Wall(ctx)
	if err != nil || value != 7 {
		t.Fatalf("wall=%d err=%v", value, err)
	}

	replayer := NewRecording(&stepClock{step: 7}, replay.Replay(journal.Facts()), 1)
	replayed, err := replayer.Wall(ctx)
	if err != nil || replayed != 7 {
		t.Fatalf("回放 wall=%d err=%v", replayed, err)
	}
}

func TestRecordingPropagatesFailures(t *testing.T) {
	ctx := context.Background()
	journal := replay.Record()
	recorder := NewRecording(&stepClock{fail: errno.Overflow}, journal, 1)
	if _, err := recorder.Monotonic(ctx); !errors.Is(err, errno.Overflow) {
		t.Fatalf("失败应上报: %v", err)
	}
	if err := recorder.Sleep(ctx, 1); !errors.Is(err, errno.Overflow) {
		t.Fatalf("等待失败应上报: %v", err)
	}

	replayer := NewRecording(&Virtual{}, replay.Replay(journal.Facts()), 1)
	if _, err := replayer.Monotonic(ctx); err == nil {
		t.Fatal("回放失败事实应报错")
	}
	if err := replayer.Sleep(ctx, 1); err == nil {
		t.Fatal("回放失败等待应报错")
	}
}

func TestDecodeTimeRejectsWrongLength(t *testing.T) {
	for _, data := range [][]byte{nil, {}, {1}, make([]byte, 7), make([]byte, 9)} {
		if _, err := decodeTime(data); !errors.Is(err, errno.Invalid) {
			t.Fatalf("长度 %d 应报 EINVAL: %v", len(data), err)
		}
	}
	if value, err := decodeTime(encodeTime(12345)); err != nil || value != 12345 {
		t.Fatalf("往返失败: %d %v", value, err)
	}
}

func TestSetElapsedToValidatesState(t *testing.T) {
	virtual := &Virtual{}
	if err := virtual.SetElapsedTo(100); err != nil {
		t.Fatal(err)
	}
	if elapsed := elapsedOf(t, virtual); elapsed != 100 {
		t.Fatalf("elapsed=%d want 100", elapsed)
	}

	if err := virtual.SetElapsedTo(100); err != nil {
		t.Fatal(err)
	}
	if elapsed := elapsedOf(t, virtual); elapsed != 100 {
		t.Fatalf("重复设置后 elapsed=%d", elapsed)
	}

	if err := virtual.SetElapsedTo(10); err != nil {
		t.Fatal(err)
	}
	if elapsed := elapsedOf(t, virtual); elapsed != 10 {
		t.Fatalf("回退后 elapsed=%d", elapsed)
	}

	if err := virtual.SetElapsedTo(maxNanoseconds + 1); !errors.Is(err, errno.Overflow) {
		t.Fatalf("溢出应报 EOVERFLOW: %v", err)
	}

	virtual.Close()
	if err := virtual.SetElapsedTo(1); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后应报 ECANCELED: %v", err)
	}
}

type plainClock struct{ value uint64 }

func (c *plainClock) Monotonic(context.Context) (uint64, error) { return c.value, nil }
func (c *plainClock) Wall(context.Context) (int64, error) {
	if c.value > 1<<63-1 {
		return 0, errno.Overflow
	}
	return int64(c.value), nil
}
func (c *plainClock) Sleep(_ context.Context, nanoseconds uint64) error {
	c.value += nanoseconds
	return nil
}

func TestReplayRequiresAlignableClock(t *testing.T) {
	ctx := context.Background()
	journal := replay.Record()
	recorder := NewRecording(&Virtual{}, journal, 1)
	if err := recorder.Sleep(ctx, 250); err != nil {
		t.Fatal(err)
	}

	replayer := NewRecording(&plainClock{}, replay.Replay(journal.Facts()), 1)
	if err := replayer.Sleep(ctx, 250); !errors.Is(err, errno.NotSupported) {
		t.Fatalf("不可对齐的时钟应报 ENOTSUP: %v", err)
	}

	zeroJournal := replay.Record()
	if err := NewRecording(&Virtual{}, zeroJournal, 1).Sleep(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if err := NewRecording(&plainClock{}, replay.Replay(zeroJournal.Facts()), 1).Sleep(ctx, 0); err != nil {
		t.Fatalf("零时长等待应放行: %v", err)
	}
}

func TestAdvanceToRecordedRejectsMalformedFacts(t *testing.T) {
	ctx := context.Background()
	recorder := NewRecording(&Virtual{}, replay.Record(), 1)
	for _, tc := range []struct {
		label   string
		output  []byte
		request []byte
	}{
		{"长度不足", make([]byte, 8), encodeTime(1)},
		{"长度超出", make([]byte, 24), encodeTime(1)},
		{"终点早于起点", append(encodeTime(500), encodeTime(100)...), encodeTime(400)},
		{"时长与请求不符", append(encodeTime(0), encodeTime(500)...), encodeTime(100)},
	} {
		err := recorder.advanceToRecorded(ctx, tc.output, tc.request)
		if err == nil {
			t.Errorf("%s: 应失败", tc.label)
		}
	}

	if err := recorder.advanceToRecorded(ctx,
		append(encodeTime(0), encodeTime(500)...), encodeTime(500)); err != nil {
		t.Fatalf("正常事实应接受: %v", err)
	}
}
