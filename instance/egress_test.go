package instance

import (
	"context"
	"io"
	stdnet "net"
	"net/netip"
	"os"
	"testing"

	platformnet "github.com/Cosmic-Developers-Union/assistant-sandbox/platform/network"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	vmnet "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/net"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
)

// TestGuestEgressObeysPolicy 验证 M09 的完整客体路径:真实 ELF 经
// socket/connect 访问策略允许的宿主 TCP 服务,拒绝目标不触达宿主。
func TestGuestEgressObeysPolicy(t *testing.T) {
	image, err := os.ReadFile("../internal/guest/testdata/m-egress")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn stdnet.Conn) {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}(conn)
		}
	}()
	port := uint16(listener.Addr().(*stdnet.TCPAddr).Port) //nolint:gosec // 内核分配的端口在 uint16 范围
	gateway := netip.MustParseAddr(vmnet.DefaultGatewayIPv4)
	policy := vmnet.NewEgressPolicy(vmnet.EgressRule{
		Protocol: vmnet.ProtocolTCP,
		Prefix:   netip.MustParsePrefix("10.0.2.2/32"),
		PortMin:  port,
		PortMax:  port,
	})
	machine, err := New(t.Context(), Options{
		Deterministic: true, Seed: 1,
		Session:     resources.Limits{Memory: 128 << 20, Work: 1 << 32},
		Process:     resources.Limits{Memory: 64 << 20, Work: 1 << 32},
		HostNetwork: platformnet.Dialer{},
		Egress:      policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = machine.Close(context.Background()) }()
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
	run := func(ip string, targetPort uint16) uint8 {
		process, err := connection.Exec(t.Context(), ExecSpec{
			Program: "/probe",
			Args:    []string{ip, itoaPort(targetPort)},
			Dir:     "/work",
			Streams: fd.Streams{},
		})
		if err != nil {
			t.Fatal(err)
		}
		state, err := connection.Wait(t.Context(), process.ID)
		if err != nil {
			t.Fatal(err)
		}
		return state.ExitCode
	}
	if code := run(gateway.String(), port); code != 0 {
		for _, entry := range machine.Kernel().Trace().Entries() {
			if entry.Result < 0 {
				t.Logf("trace pid=%d call=%d result=%d", entry.PID, entry.Number, entry.Result)
			}
		}
		t.Fatalf("允许的宿主服务 exit=%d want 0", code)
	}
	if code := run("10.0.2.3", port); code != 20 {
		t.Fatalf("拒绝地址 exit=%d want 20", code)
	}
	if code := run(gateway.String(), port+1); code != 20 {
		t.Fatalf("拒绝端口 exit=%d want 20", code)
	}

	// 未配置出口的 VM 同样拒绝。
	plain, err := New(t.Context(), Options{Deterministic: true, Seed: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = plain.Close(context.Background()) }()
	plainProbe, err := plain.Filesystem().Open(t.Context(), "/probe", vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plainProbe.Write(image); err != nil {
		t.Fatal(err)
	}
	if err := plainProbe.Close(); err != nil {
		t.Fatal(err)
	}
	plainConnection, err := plain.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	process, err := plainConnection.Exec(t.Context(), ExecSpec{Program: "/probe", Args: []string{gateway.String(), itoaPort(port)}})
	if err != nil {
		t.Fatal(err)
	}
	state, err := plainConnection.Wait(t.Context(), process.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ExitCode != 20 {
		t.Fatalf("未配置出口 exit=%d want 20", state.ExitCode)
	}
}

func itoaPort(port uint16) string {
	if port == 0 {
		return "0"
	}
	var buffer [5]byte
	index := len(buffer)
	for port > 0 {
		index--
		buffer[index] = byte('0' + port%10)
		port /= 10
	}
	return string(buffer[index:])
}
