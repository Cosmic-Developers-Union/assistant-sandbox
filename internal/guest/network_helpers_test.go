package guest_test

import (
	stdnet "net"
	"net/netip"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
	platformnet "github.com/Cosmic-Developers-Union/assistant-sandbox/platform/network"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/net"
)

// guestGateway 是客体可见的宿主网关(M09);DNS 与 TLS 验收都经它
// 进入受策略约束的宿主出口。
const guestGateway = "10.0.2.2"

// startGuestDNS 启动最小 UDP DNS 服务,供 M10/M11 的客体运行时解析:
// guest.test 与 tls.test 的 A 记录指向客体网关,missing.test 返回
// NXDOMAIN,AAAA 返回空 NOERROR。
func startGuestDNS(t *testing.T) uint16 {
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
			response := guestDNSResponse(buffer[:read])
			if len(response) > 0 {
				_, _ = packet.WriteTo(response, from)
			}
		}
	}()
	return uint16(packet.LocalAddr().(*stdnet.UDPAddr).Port) //nolint:gosec // 内核分配端口在 uint16 范围
}

func guestDNSResponse(query []byte) []byte {
	if len(query) < 12 {
		return nil
	}
	offset := 12
	for offset < len(query) && query[offset] != 0 {
		if query[offset] > 63 || offset+int(query[offset])+1 > len(query) {
			return nil
		}
		offset += int(query[offset]) + 1
	}
	if offset+5 > len(query) {
		return nil
	}
	name := string(query[12:offset])
	offset++
	qtype := binaryUint16(query[offset : offset+2])
	question := append([]byte(nil), query[12:offset+4]...)
	flags := uint16(0x8180)
	answers := uint16(0)
	var answer []byte
	switch {
	case (name == "\x05guest\x04test" || name == "\x03tls\x04test") && qtype == 1:
		answers = 1
		answer = []byte{0xc0, 0x0c, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x3c, 0x00, 0x04, 10, 0, 2, 2}
	case (name == "\x05guest\x04test" || name == "\x03tls\x04test") && qtype == 28:
		// 空 NOERROR:客户端回退到 IPv4。
	default:
		flags = 0x8183 // NXDOMAIN
	}
	response := make([]byte, 12, 12+len(question)+len(answer))
	copy(response[0:2], query[0:2])
	putUint16(response[2:4], flags)
	putUint16(response[4:6], 1)
	putUint16(response[6:8], answers)
	response = append(response, question...)
	response = append(response, answer...)
	return response
}

func binaryUint16(raw []byte) uint16 {
	return uint16(raw[0])<<8 | uint16(raw[1])
}

func putUint16(raw []byte, value uint16) {
	raw[0] = byte(value >> 8)
	raw[1] = byte(value)
}

// dnsEgressPolicy 允许客体访问网关的 DNS 端口,并把 53 映射到宿主
// 测试 DNS 服务;其他目标继续默认拒绝。
func dnsEgressPolicy(hostPort uint16) *net.EgressPolicy {
	policy := net.NewEgressPolicy(net.EgressRule{
		Protocol: net.ProtocolUDP,
		Prefix:   netip.MustParsePrefix(guestGateway + "/32"),
		PortMin:  53,
		PortMax:  53,
	})
	policy.PortMap = map[uint16]uint16{53: hostPort}
	return policy
}

// guestEgress 把宿主拨号器与出口策略装入 VM 选项。
func guestEgress(policy *net.EgressPolicy) func(*instance.Options) {
	return func(options *instance.Options) {
		options.Deterministic = false
		options.HostNetwork = platformnet.Dialer{}
		options.Egress = policy
	}
}
