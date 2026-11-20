package guest_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/elf"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/guest/rootfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
)

// n04Digest 锁定身份/locale 探针(重建须显式更新):静态 musl 程序,
// 经 getpwuid(2000)/getgrgid(2001)/getenv/setlocale/mbrtowc 查询。
const n04Digest = "977397ff5aa22537b26329bf2fc69e7c5c6d748ad3ef8ea2dd2a56a674370715"

// TestGuestQueriesIdentityHomeAndUTF8 验证 N04:真实客体程序从根文件
// 系统读 passwd/group、从环境读 HOME/PATH/LANG,并用 libc 完成 UTF-8
// 多字节转换;不依赖宿主身份或 locale。
func TestGuestQueriesIdentityHomeAndUTF8(t *testing.T) {
	image, err := os.ReadFile("testdata/n04-identity")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(image)
	if got := hex.EncodeToString(sum[:]); got != n04Digest {
		t.Fatalf("N04 探针摘要=%s want %s", got, n04Digest)
	}
	layout, err := rootfs.Build(context.Background(), rootfs.Options{
		Clock:   &clock.Virtual{},
		Workdir: "/work",
		User:    rootfs.User{Name: "bob", UID: 2000, GID: 2001, Home: "/home/bob", Shell: "/bin/sh"},
	})
	if err != nil {
		t.Fatal(err)
	}
	probe, err := layout.System.Open(context.Background(), "/probe", vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := probe.Write(image); err != nil {
		t.Fatal(err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	registry, err := executable.New(map[string]executable.Program{})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := vm.New(vm.Services{
		Set: capability.Set{
			FS:       layout.System,
			FD:       fd.New(),
			Identity: identity.SingleUser{},
			Clock:    &clock.Virtual{},
			Entropy:  entropy.NewSeeded(1),
		},
		Programs: registry,
		Binaries: elf.Factory{},
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = kernel.Close(context.Background()) }()
	output := &strings.Builder{}
	process, err := kernel.Begin(0, "/probe", nil, "/", layout.Environment(), fd.Streams{Output: output})
	if err != nil {
		t.Fatal(err)
	}
	status, err := kernel.RunRegistered(context.Background(), process)
	if err != nil || status.Code != 0 {
		t.Fatalf("探针退出=%+v err=%v 输出=%q(断言码见 n04-identity.c)", status, err, output.String())
	}
	const want = "N04-OK uid=2000 gid=2001 home=/home/bob lang=C.UTF-8\n"
	if output.String() != want {
		t.Fatalf("输出=%q want %q", output.String(), want)
	}
}
