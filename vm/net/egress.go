package net

import (
	"context"
	"io"
	"net/netip"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// 协议名与宿主网关默认值(M09)。10.0.2.2 是常见的 guest 宿主网关,
// 映射到宿主回环;策略仍然逐地址/端口校验。
const (
	ProtocolTCP = "tcp"
	ProtocolUDP = "udp"

	DefaultGatewayIPv4 = "10.0.2.2"
	DefaultHostIPv4    = "127.0.0.1"
)

// EgressRule 是一条宿主出口许可:协议、目标地址前缀与端口范围。
type EgressRule struct {
	Protocol string
	Prefix   netip.Prefix
	PortMin  uint16
	PortMax  uint16
}

// Matches 报告规则是否允许该目标。
func (r EgressRule) Matches(protocol string, address netip.Addr, port uint16) bool {
	if r.Protocol != "" && r.Protocol != protocol {
		return false
	}
	if !r.Prefix.Contains(address) {
		return false
	}
	if r.PortMin != 0 && port < r.PortMin {
		return false
	}
	if r.PortMax != 0 && port > r.PortMax {
		return false
	}
	return true
}

// EgressPolicy 是默认拒绝的宿主出口策略(M09):任何未被规则命中的
// 地址、端口或协议都不会触发宿主拨号。Gateway 是 guest 可见的宿主
// 网关地址,映射到 Host(默认 10.0.2.2 → 127.0.0.1)。
type EgressPolicy struct {
	Rules []EgressRule

	Gateway netip.Addr
	Host    netip.Addr
}

// NewEgressPolicy 创建默认拒绝策略。
func NewEgressPolicy(rules ...EgressRule) *EgressPolicy {
	return &EgressPolicy{
		Rules:   append([]EgressRule(nil), rules...),
		Gateway: netip.MustParseAddr(DefaultGatewayIPv4),
		Host:    netip.MustParseAddr(DefaultHostIPv4),
	}
}

// Allows 报告协议/地址/端口是否被策略允许。
func (p *EgressPolicy) Allows(protocol string, address netip.Addr, port uint16) bool {
	if p == nil || !address.IsValid() || port == 0 {
		return false
	}
	for _, rule := range p.Rules {
		if rule.Matches(protocol, address, port) {
			return true
		}
	}
	return false
}

// Target 返回策略允许的宿主目标;guest 网关映射到宿主地址。
func (p *EgressPolicy) Target(protocol string, remote Addr) (netip.Addr, uint16, bool) {
	if p == nil || remote.Domain != AFInet || remote.Port == 0 {
		return netip.Addr{}, 0, false
	}
	if !p.Allows(protocol, remote.IP, remote.Port) {
		return netip.Addr{}, 0, false
	}
	target := remote.IP
	if p.Gateway.IsValid() && remote.IP == p.Gateway && p.Host.IsValid() {
		target = p.Host
	}
	return target, remote.Port, true
}

// Resolver 是 DNS 解析端口(M10 复用):名称解析结果逐地址经过出口
// 策略,调用方不得在策略之外直接使用解析结果。
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Resolve 解析名称并返回第一个被策略允许的地址;任一候选地址都不
// 被允许时返回 ENETUNREACH,因此 DNS 答案不能绕过地址/端口策略。
func (p *EgressPolicy) Resolve(ctx context.Context, resolver Resolver, protocol, host string, port uint16) (netip.Addr, error) {
	if resolver == nil || host == "" {
		return netip.Addr{}, errno.Wrap(errno.Invalid, "解析目标无效")
	}
	addresses, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return netip.Addr{}, errno.Wrap(errno.NetworkUnreachable, "名称解析失败")
	}
	for _, address := range addresses {
		if p.Allows(protocol, address, port) {
			return address, nil
		}
	}
	return netip.Addr{}, errno.Wrap(errno.NetworkUnreachable, "DNS 答案不在出口策略内")
}

// SetEgress 装配宿主出口网络与默认拒绝策略。两者必须同时提供;
// 未装配时非回环连接一律 ENETUNREACH。
func (n *Namespace) SetEgress(network Network, policy *EgressPolicy) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.egress = network
	n.policy = policy
}

// dialEgress 在策略允许后拨号宿主,并把宿主连接桥接为 guest 回环流。
func (n *Namespace) dialEgress(ctx context.Context, local, remote Addr) (*TCPConn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n.mu.Lock()
	network := n.egress
	policy := n.policy
	closed := n.closed
	n.mu.Unlock()
	if closed {
		return nil, errno.BadHandle
	}
	if network == nil || policy == nil {
		return nil, errno.Wrap(errno.NetworkUnreachable, "宿主出口未配置")
	}
	target, port, allowed := policy.Target(ProtocolTCP, remote)
	if !allowed {
		return nil, errno.Wrap(errno.NetworkUnreachable, "出口策略拒绝目标")
	}
	host, err := network.Dial(ctx, "tcp", joinHostPort(target.String(), port))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errno.Wrap(errno.ConnRefused, "宿主服务不可达")
	}
	client, server := newStreamPair(local, remote)
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		_ = host.Close()
		_ = client.Close()
		_ = server.Close()
		return nil, errno.BadHandle
	}
	if n.hostConns == nil {
		n.hostConns = map[io.Closer]struct{}{}
	}
	n.hostConns[host] = struct{}{}
	n.mu.Unlock()
	go pumpHost(host, server, func() {
		n.mu.Lock()
		delete(n.hostConns, host)
		n.mu.Unlock()
	})
	return client, nil
}

// pumpHost 双向转发宿主连接与 guest 流端;任一方向结束即关闭宿主
// 连接,避免半关闭后遗留宿主 goroutine。
func pumpHost(host io.ReadWriteCloser, guest *streamConn, done func()) {
	defer done()
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			_ = host.Close()
			_ = guest.Close()
		})
	}
	finished := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(host, guest)
		finished <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(guest, host)
		_ = guest.Shutdown(ShutdownWrite)
		finished <- struct{}{}
	}()
	<-finished
	closeBoth()
	<-finished
}

// joinHostPort 避免在 vm/net 引入宿主 net 包。
func joinHostPort(host string, port uint16) string {
	return host + ":" + itoaPort(port)
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
