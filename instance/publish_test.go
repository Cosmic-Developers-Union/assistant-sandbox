package instance

import (
	"bytes"
	stdnet "net"
	"os"
	"testing"
	"time"

	platformnet "github.com/Cosmic-Developers-Union/assistant-sandbox/platform/network"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	vmnet "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/net"
)

// TestGuestPublishedPortExplicitlyMapped 验证 M13 的完整客体路径:
// 真实 ELF 在客体监听,只有显式 Publish 的宿主端口可访问;撤销后
// 宿主入口关闭。
func TestGuestPublishedPortExplicitlyMapped(t *testing.T) {
	image, err := os.ReadFile("../internal/guest/testdata/m-publish")
	if err != nil {
		t.Fatal(err)
	}
	machine, err := New(t.Context(), Options{Deterministic: true, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = machine.Close(t.Context()) }()
	probe, err := machine.Filesystem().Open(t.Context(), "/probe", vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := probe.Write(image); err != nil {
		t.Fatal(err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	connection, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	const guestPort = 7101
	guest := vmnet.Addr{Domain: vmnet.AFInet, IP: vmnet.LoopbackIPv4, Port: guestPort}
	output := &bytes.Buffer{}
	process, err := connection.Exec(t.Context(), ExecSpec{
		Program: "/probe",
		Args:    []string{"7101"},
		Dir:     "/work",
		Streams: fd.Streams{Output: output},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 客体监听建立前 Publish 报 ECONNREFUSED;按 Linux 部署惯例重试,
	// 只有显式发布后才创建宿主入口。
	var publication *vmnet.Publication
	deadline := time.Now().Add(5 * time.Second)
	for {
		publication, err = machine.Publish(t.Context(), platformnet.Dialer{}, "127.0.0.1:0", guest)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("客体监听未就绪: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	host, err := stdnet.Dial("tcp", publication.Addr())
	if err != nil {
		t.Fatalf("发布端口不可访问: %v", err)
	}
	if _, err := host.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 4)
	read, err := host.Read(reply)
	if err != nil || string(reply[:read]) != "pong" {
		t.Fatalf("宿主收到=%q err=%v", reply[:read], err)
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := connection.Wait(t.Context(), process.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ExitCode != 0 || output.String() != "M13-OK\n" {
		t.Fatalf("客体服务=%+v 输出=%q", state, output.String())
	}

	// 撤销映射:宿主入口关闭,且重复撤销幂等。
	if err := publication.Revoke(); err != nil {
		t.Fatal(err)
	}
	if err := publication.Revoke(); err != nil {
		t.Fatal(err)
	}
	revokedDeadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := stdnet.Dial("tcp", publication.Addr())
		if err != nil {
			break
		}
		_ = conn.Close()
		if time.Now().After(revokedDeadline) {
			t.Fatal("撤销后宿主入口仍可连接")
		}
		time.Sleep(time.Millisecond)
	}

	// 未监听客体端口的映射在打开宿主监听前就被拒绝。
	if _, err := machine.Publish(t.Context(), platformnet.Dialer{}, "127.0.0.1:0", vmnet.Addr{
		Domain: vmnet.AFInet, IP: vmnet.LoopbackIPv4, Port: 7999,
	}); err == nil {
		t.Fatal("未监听的客体端口不应发布成功")
	}
}
