package replay

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func TestRecordAndReplayRoundTrip(t *testing.T) {
	journal := Record()
	ctx := context.Background()
	calls := 0
	run := func(context.Context) ([]byte, string, error) {
		calls++
		return []byte("output"), "", nil
	}
	if _, err := Run(ctx, journal, 2, OpBridgeCall, []byte("in"), run); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}

	playback := Replay(journal.Facts())
	output, err := Run(ctx, playback, 2, OpBridgeCall, []byte("in"), func(context.Context) ([]byte, string, error) {
		calls++
		t.Fatal("回放不得触发真实副作用")
		return nil, "", nil
	})
	if err != nil || string(output) != "output" {
		t.Fatalf("output=%q err=%v", output, err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
	if playback.Remaining() != 0 {
		t.Fatalf("remaining=%d", playback.Remaining())
	}
}

func TestErrorPathsAreRecorded(t *testing.T) {
	journal := Record()
	ctx := context.Background()
	_, err := Run(ctx, journal, 3, OpStdin, []byte("x"), func(context.Context) ([]byte, string, error) {
		return nil, "io", errno.Limit
	})
	if !errors.Is(err, errno.Limit) {
		t.Fatal(err)
	}
	playback := Replay(journal.Facts())
	_, err = Run(ctx, playback, 3, OpStdin, []byte("x"), func(context.Context) ([]byte, string, error) {
		t.Fatal("错误路径回放不得重放副作用")
		return nil, "", nil
	})
	if !errors.Is(err, errno.Invalid) {
		t.Fatalf("err=%v", err)
	}
}

func TestReplayStopsAtFirstDivergence(t *testing.T) {
	journal := Record()
	ctx := context.Background()
	_, err := Run(ctx, journal, 2, OpEntropy, []byte{1}, func(context.Context) ([]byte, string, error) {
		return []byte{2}, "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	playback := Replay(journal.Facts())

	_, err = Run(ctx, playback, 2, OpEntropy, []byte{9}, func(context.Context) ([]byte, string, error) {
		t.Fatal("分歧后不得访问真实资源")
		return nil, "", nil
	})
	var mismatch *MismatchError
	if !errors.As(err, &mismatch) || mismatch.Reason != "输入参数不一致" {
		t.Fatalf("err=%v", err)
	}

	playback = Replay(journal.Facts())
	_, err = Run(ctx, playback, 2, OpStdin, []byte{1}, func(context.Context) ([]byte, string, error) { return nil, "", nil })
	if !errors.As(err, &mismatch) || mismatch.Reason != "顺序或进程不一致" {
		t.Fatalf("err=%v", err)
	}

	playback = Replay(journal.Facts())
	_, err = Run(ctx, playback, 2, OpEntropy, []byte{1}, func(context.Context) ([]byte, string, error) { return nil, "", nil })
	_, err2 := Run(ctx, playback, 2, OpEntropy, []byte{1}, func(context.Context) ([]byte, string, error) { return nil, "", nil })
	if !errors.Is(err2, errno.NotFound) {
		t.Fatalf("err=%v err2=%v", err, err2)
	}
}

func TestRecordModeRejectsNextAndViceVersa(t *testing.T) {
	ctx := context.Background()
	record := Record()
	if err := record.Record(ctx, Fact{}); err != nil {
		t.Fatal(err)
	}
	if _, err := record.Next(ctx, Fact{}); !errors.Is(err, errno.NotSupported) {
		t.Fatal(err)
	}
	playback := Replay(nil)
	if err := playback.Record(ctx, Fact{}); !errors.Is(err, errno.NotSupported) {
		t.Fatal(err)
	}
	if _, err := Run(ctx, nil, 1, "op", nil, nil); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
}

func TestFactPayloadsAreCloned(t *testing.T) {
	journal := Record()
	ctx := context.Background()
	input := []byte("input")
	_, err := Run(ctx, journal, 1, OpStdin, input, func(context.Context) ([]byte, string, error) {
		return []byte("out"), "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	input[0] = 'X'
	if string(journal.Facts()[0].Input) != "input" {
		t.Fatal("输入未克隆")
	}
}
