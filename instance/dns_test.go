package instance

import (
	"bytes"
	"context"
	"encoding/binary"
	stdnet "net"
	"net/netip"
	"os"
	"testing"
	"time"

	platformnet "github.com/Cosmic-Developers-Union/assistant-sandbox/platform/network"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	vmnet "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/net"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
)

// startDNS 启动最小 UDP DNS 服务:libc.test A → 192.0.2.7,
// missing.test → NXDOMAIN,AAAA 查询返回空 NOERROR。
func startDNS(t *testing.T) uint16 {
	t.Helper()
	packet, err := stdnet.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = packet.Close() })
	go func() {
		buffer := make([]byte, 1500)
		for {
			read, from, err := packet.ReadFrom(buffer)
			if err != nil {
				return
			}
			response := dnsResponse(buffer[:read])
			if len(response) > 0 {
				_, _ = packet.WriteTo(response, from)
			}
		}
	}()
	return uint16(packet.LocalAddr().(*stdnet.UDPAddr).Port) //nolint:gosec // 内核分配端口在 uint16 范围
}

func dnsResponse(query []byte) []byte {
	if len(query) < 12 {
		return nil
	}
	offset := 12
	for offset < len(query) && query[offset] != 0 {
		offset += int(query[offset]) + 1
	}
	if offset+5 > len(query) {
		return nil
	}
	name := string(query[12:offset])
	offset++ // 终结零
	if offset+4 > len(query) {
		return nil
	}
	qtype := binary.BigEndian.Uint16(query[offset : offset+2])
	question := append([]byte(nil), query[12:offset+4]...)
	flags := uint16(0x8180)
	answers := uint16(0)
	var answer []byte
	switch {
	case name == "\x04libc\x04test" && qtype == 1:
		answers = 1
		answer = []byte{0xc0, 0x0c, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x3c, 0x00, 0x04, 192, 0, 2, 7}
	case name == "\x04libc\x04test" && qtype == 28:
		// 空 NOERROR
	default:
		flags = 0x8183 // NXDOMAIN
	}
	response := make([]byte, 12, 12+len(question)+len(answer))
	copy(response[0:2], query[0:2])
	binary.BigEndian.PutUint16(response[2:4], flags)
	binary.BigEndian.PutUint16(response[4:6], 1)
	binary.BigEndian.PutUint16(response[6:8], answers)
	response = append(response, question...)
	response = append(response, answer...)
	return response
}

// TestGuestDNSResolvesThroughPolicy 验证 M10 的 libc 路径:客体 musl
// resolver 读取 /etc/resolv.conf,经受策略约束的 UDP 宿主出口解析
// 成功与 NXDOMAIN;DNS 答案不能绕过连接策略。
func TestGuestDNSResolvesThroughPolicy(t *testing.T) {
	image, err := os.ReadFile("../internal/guest/testdata/m-dns")
	if err != nil {
		t.Fatal(err)
	}
	hostPort := startDNS(t)
	policy := vmnet.NewEgressPolicy(vmnet.EgressRule{
		Protocol: vmnet.ProtocolUDP,
		Prefix:   netip.MustParsePrefix("10.0.2.2/32"),
		PortMin:  53,
		PortMax:  53,
	})
	policy.PortMap = map[uint16]uint16{53: hostPort}
	machine, err := New(t.Context(), Options{
		Deterministic: false, TraceLimit: 100000,
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
	t.Logf("resolv.conf=%q", readProcFile(t, machine, t.Context(), "/etc/resolv.conf"))
	output := &bytes.Buffer{}
	runCtx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	process, err := connection.Exec(runCtx, ExecSpec{Program: "/probe", Dir: "/work", Streams: fd.Streams{Output: output}})
	if err != nil {
		t.Fatal(err)
	}
	state, err := connection.Wait(t.Context(), process.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ExitCode != 0 || output.String() != "M10-OK\n" {
		t.Fatalf("DNS 探针=%+v 输出=%q(断言码见 m-dns.c)", state, output.String())
	}
}
