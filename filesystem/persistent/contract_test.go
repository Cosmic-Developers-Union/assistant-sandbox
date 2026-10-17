package persistent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/storage"
)

var (
	_ storage.Sessions  = (*Store)(nil)
	_ storage.Snapshots = (*Store)(nil)
)

func TestSatisfiesStorageContractThroughInterface(t *testing.T) {
	ctx := context.Background()
	var sessions storage.Sessions = newStore(t)
	var snapshots storage.Snapshots = newStore(t)

	if _, err := sessions.Load(ctx, "s"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("未保存应报 ENOENT: %v", err)
	}
	if err := sessions.Save(ctx, "s", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	data, err := sessions.Load(ctx, "s")
	if err != nil || string(data) != "v1" {
		t.Fatalf("读回=%q err=%v", data, err)
	}
	if err := sessions.Save(ctx, "s", []byte("v2")); err != nil {
		t.Fatal(err)
	}
	if data, err = sessions.Load(ctx, "s"); err != nil || string(data) != "v2" {
		t.Fatalf("覆盖后=%q err=%v", data, err)
	}
	if err := sessions.Delete(ctx, "s"); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Load(ctx, "s"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("删除后应报 ENOENT: %v", err)
	}

	digest, err := snapshots.Put(ctx, []byte("body"))
	if err != nil {
		t.Fatal(err)
	}
	restored, err := snapshots.Get(ctx, digest)
	if err != nil || string(restored) != "body" {
		t.Fatalf("快照读回=%q err=%v", restored, err)
	}

	again, err := snapshots.Put(ctx, []byte("body"))
	if err != nil || again != digest {
		t.Fatalf("重复发布应幂等: %q vs %q err=%v", again, digest, err)
	}
}

func TestInterchangeableWithMemoryReference(t *testing.T) {
	ctx := context.Background()
	type behavior struct {
		loadMissing   bool
		roundTrip     string
		overwrite     string
		deleteMissing bool
		digestStable  bool
	}
	run := func(sessions storage.Sessions, snapshots storage.Snapshots) behavior {
		out := behavior{}
		if _, err := sessions.Load(ctx, "k"); errors.Is(err, errno.NotFound) {
			out.loadMissing = true
		}
		_ = sessions.Save(ctx, "k", []byte("one"))
		if data, err := sessions.Load(ctx, "k"); err == nil {
			out.roundTrip = string(data)
		}
		_ = sessions.Save(ctx, "k", []byte("two"))
		if data, err := sessions.Load(ctx, "k"); err == nil {
			out.overwrite = string(data)
		}
		_ = sessions.Delete(ctx, "k")
		if err := sessions.Delete(ctx, "k"); errors.Is(err, errno.NotFound) {
			out.deleteMissing = true
		}
		first, err1 := snapshots.Put(ctx, []byte("same"))
		second, err2 := snapshots.Put(ctx, []byte("same"))
		out.digestStable = err1 == nil && err2 == nil && first == second && strings.HasPrefix(first, digestPrefix)
		return out
	}
	reference := run(storage.New(1<<20), storage.New(1<<20))
	subject := run(newStore(t), newStore(t))
	if reference != subject {
		t.Fatalf("行为不一致:\n参考实现 %+v\n本实现   %+v", reference, subject)
	}
}
