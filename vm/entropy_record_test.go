package vm

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/bridge"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/replay"
)

func entropyKernel(t *testing.T, journal *replay.Memory, source entropy.Source) (*Kernel, *[]byte) {
	t.Helper()
	var read []byte
	registry, err := executable.New(map[string]executable.Program{
		"reader": executable.Func(func(ctx context.Context, p proc.Proc) (uint8, error) {
			random, err := p.Resources.Random()
			if err != nil {
				return 0, err
			}
			buffer := make([]byte, 4)
			if _, err := random.Read(ctx, buffer); err != nil {
				return 0, err
			}
			read = buffer
			return 0, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{
		Set:      capability.Set{Entropy: source},
		Programs: registry,
		Journal:  journal,
	}, 4)
	if err != nil {
		t.Fatal(err)
	}
	return kernel, &read
}

func TestKernelRecordsEntropyThroughCapability(t *testing.T) {
	ctx := context.Background()
	journal := replay.Record()
	kernel, read := entropyKernel(t, journal, entropy.NewSeeded(7))
	if _, err := kernel.Exec(ctx, "reader", nil); err != nil {
		t.Fatal(err)
	}
	if len(*read) != 4 {
		t.Fatalf("程序应读到 4 字节: %v", *read)
	}
	facts := journal.Facts()
	if len(facts) != 1 || facts[0].Operation != replay.OpEntropy {
		t.Fatalf("应记录一条 entropy 事实: %+v", facts)
	}

	replayKernel, replayed := entropyKernel(t, replay.Replay(facts), &failingSource{})
	if _, err := replayKernel.Exec(ctx, "reader", nil); err != nil {
		t.Fatalf("回放不应触达真实源: %v", err)
	}
	if string(*replayed) != string(*read) {
		t.Fatalf("回放字节不一致: %x vs %x", *replayed, *read)
	}
}

func TestKernelEntropyRecordingIsOptional(t *testing.T) {
	kernel, read := entropyKernel(t, nil, entropy.NewSeeded(7))
	if _, err := kernel.Exec(context.Background(), "reader", nil); err != nil {
		t.Fatal(err)
	}
	if len(*read) != 4 {
		t.Fatalf("未配置 Journal 时应直通真实源: %v", *read)
	}
}

func TestKernelWithoutEntropyStillReportsNotImplemented(t *testing.T) {
	registry, err := executable.New(map[string]executable.Program{
		"reader": executable.Func(func(ctx context.Context, p proc.Proc) (uint8, error) {
			if _, err := p.Resources.Random(); err == nil {
				return 0, errors.New("未配置随机源时必须失败")
			}
			return 0, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Programs: registry, Journal: replay.Record()}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Exec(context.Background(), "reader", nil); err != nil {
		t.Fatal(err)
	}
}

type failingSource struct{}

func (failingSource) Read(context.Context, []byte) (int, error) {
	return 0, errors.New("真实随机源不应被读取")
}

type stubBridge struct {
	calls int
	reply string
	err   error
}

func (s *stubBridge) Call(_ context.Context, _ uint64, _ string, _ []byte) ([]byte, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return []byte(s.reply), nil
}

func bridgeKernel(t *testing.T, journal *replay.Memory, host bridge.Bridge) (*Kernel, *stubBridge) {
	t.Helper()
	stub, _ := host.(*stubBridge)
	registry, err := executable.New(map[string]executable.Program{
		"caller": executable.Func(func(ctx context.Context, p proc.Proc) (uint8, error) {
			bridgeHandle, err := p.Resources.Host()
			if err != nil {
				return 0, err
			}
			result, err := bridgeHandle.Call(ctx, uint64(p.ID), "demo.op", []byte("in"))
			if err != nil {
				return 0, err
			}
			if string(result) != "out" {
				return 0, errors.New("桥返回不符: " + string(result))
			}
			return 0, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{
		Set:      capability.Set{Bridge: host},
		Programs: registry,
		Journal:  journal,
	}, 4)
	if err != nil {
		t.Fatal(err)
	}
	return kernel, stub
}

func TestKernelRecordsBridgeThroughCapability(t *testing.T) {
	ctx := context.Background()
	journal := replay.Record()
	source := &stubBridge{reply: "out"}
	kernel, _ := bridgeKernel(t, journal, source)
	if _, err := kernel.Exec(ctx, "caller", nil); err != nil {
		t.Fatal(err)
	}
	if source.calls != 1 {
		t.Fatalf("记录模式应调用一次宿主桥: %d", source.calls)
	}
	facts := journal.Facts()
	if len(facts) != 1 || facts[0].Operation != replay.OpBridgeCall {
		t.Fatalf("应记录一条桥调用事实: %+v", facts)
	}
	replaySource := &stubBridge{reply: "out"}
	replayKernel, _ := bridgeKernel(t, replay.Replay(facts), replaySource)
	if _, err := replayKernel.Exec(ctx, "caller", nil); err != nil {
		t.Fatal(err)
	}
	if replaySource.calls != 0 {
		t.Fatalf("回放不得触达宿主桥: %d", replaySource.calls)
	}
}

func TestKernelBridgeRecordingIsOptional(t *testing.T) {
	source := &stubBridge{reply: "out"}
	kernel, _ := bridgeKernel(t, nil, source)
	if _, err := kernel.Exec(context.Background(), "caller", nil); err != nil {
		t.Fatal(err)
	}
	if source.calls != 1 {
		t.Fatalf("未配置 Journal 时应直通: %d", source.calls)
	}
}

func TestKernelWritesIdentityIntoFactHeader(t *testing.T) {
	journal := replay.Record()
	registry, err := executable.New(map[string]executable.Program{
		"noop": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := replay.Header{
		Image: "sha256:image", Program: "/bin/init", ABI: "builtin-go", InitialDigest: "sha256:initial",
	}
	if _, err := New(Services{
		Set:      capability.Set{Entropy: entropy.NewSeeded(1)},
		Programs: registry,
		Journal:  journal,
		Identity: identity,
	}, 4); err != nil {
		t.Fatal(err)
	}
	header, ok := journal.Header()
	if !ok {
		t.Fatal("记录模式必须写入事实头")
	}
	if header.Format != replay.HeaderFormat {
		t.Fatalf("header=%+v", header)
	}

	if header.Mode != journal.Mode() {
		t.Fatalf("header.Mode=%q journal.Mode=%q", header.Mode, journal.Mode())
	}
	if header.Image != identity.Image || header.Program != identity.Program ||
		header.ABI != identity.ABI || header.InitialDigest != identity.InitialDigest {
		t.Fatalf("身份字段未写入: %+v", header)
	}
}

func TestKernelDoesNotOverwriteReplayHeader(t *testing.T) {
	recorded := replay.Header{
		Format: replay.HeaderFormat, Mode: replay.ModeRecord,
		Image: "sha256:recorded", ABI: "builtin-go",
	}
	journal := replay.Replay(nil)
	registry, err := executable.New(map[string]executable.Program{
		"noop": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{
		Set:      capability.Set{Entropy: entropy.NewSeeded(1)},
		Programs: registry,
		Journal:  journal,
		Identity: replay.Header{Image: "sha256:current"},
	}, 4)
	if err != nil {
		t.Fatal(err)
	}
	_ = kernel

	if _, ok := journal.Header(); ok {
		t.Fatal("内核不得给回放 Journal 写事实头")
	}

	if err := replay.CheckHeader(recorded, replay.Header{Format: replay.HeaderFormat, Image: "sha256:current"}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("身份漂移必须拒绝: %v", err)
	}
}
