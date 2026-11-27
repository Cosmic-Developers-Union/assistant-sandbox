//go:build linux && amd64

package instance

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmnet "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/net"
)

const nativeEgressSocket = "/.assistant-egress.sock"

type nativeEgress struct {
	listener net.Listener
	network  vmnet.Network
	policy   *vmnet.EgressPolicy
	ctx      context.Context
	cancel   context.CancelFunc

	mu    sync.Mutex
	conns map[io.Closer]struct{}
	done  chan struct{}
}

func startNativeEgress(root string, network vmnet.Network, policy *vmnet.EgressPolicy) (*nativeEgress, error) {
	if network == nil || policy == nil {
		return nil, fmt.Errorf("native egress requires both HostNetwork and Egress: %w", errno.Invalid)
	}
	ctx, cancel := context.WithCancel(context.Background())
	listener, err := net.Listen("unix", filepath.Join(root, nativeEgressSocket))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("listen on guest egress socket: %w", err)
	}
	proxy := &nativeEgress{
		listener: listener, network: network, policy: policy,
		ctx: ctx, cancel: cancel, conns: map[io.Closer]struct{}{}, done: make(chan struct{}),
	}
	go proxy.acceptLoop()
	return proxy, nil
}

func (p *nativeEgress) environment() map[string]string {
	return map[string]string{
		"HTTP_PROXY":  "http://127.0.0.1:18080",
		"HTTPS_PROXY": "http://127.0.0.1:18080",
		"ALL_PROXY":   "http://127.0.0.1:18080",
		"NO_PROXY":    "127.0.0.1,localhost",
	}
}

func (p *nativeEgress) acceptLoop() {
	defer close(p.done)
	for {
		connection, err := p.listener.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		if p.ctx.Err() != nil {
			p.mu.Unlock()
			_ = connection.Close()
			return
		}
		p.conns[connection] = struct{}{}
		p.mu.Unlock()
		go p.serve(connection)
	}
}

func (p *nativeEgress) serve(connection net.Conn) {
	defer func() {
		_ = connection.Close()
		p.mu.Lock()
		delete(p.conns, connection)
		p.mu.Unlock()
	}()
	target, err := readNativeEgressTarget(connection)
	if err != nil {
		_, _ = io.WriteString(connection, "DENIED\n")
		return
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		_, _ = io.WriteString(connection, "DENIED\n")
		return
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		_, _ = io.WriteString(connection, "DENIED\n")
		return
	}
	address, err := p.resolve(host)
	if err != nil {
		_, _ = io.WriteString(connection, "DENIED\n")
		return
	}
	mapped, mappedPort, allowed := p.policy.Target(vmnet.ProtocolTCP, vmnet.Addr{
		Domain: vmnet.AFInet, IP: address, Port: uint16(port),
	})
	if !allowed {
		_, _ = io.WriteString(connection, "DENIED\n")
		return
	}
	upstream, err := p.network.Dial(p.ctx, vmnet.ProtocolTCP, net.JoinHostPort(mapped.String(), strconv.FormatUint(uint64(mappedPort), 10)))
	if err != nil {
		_, _ = io.WriteString(connection, "DENIED\n")
		return
	}
	defer func() { _ = upstream.Close() }()
	p.mu.Lock()
	p.conns[upstream] = struct{}{}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.conns, upstream)
		p.mu.Unlock()
	}()
	if _, err := io.WriteString(connection, "OK\n"); err != nil {
		return
	}
	finished := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, connection)
		finished <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(connection, upstream)
		finished <- struct{}{}
	}()
	<-finished
	_ = connection.Close()
	_ = upstream.Close()
	<-finished
}

func (p *nativeEgress) resolve(host string) (netip.Addr, error) {
	if address, err := netip.ParseAddr(host); err == nil {
		return address.Unmap(), nil
	}
	addresses, err := net.DefaultResolver.LookupNetIP(p.ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return netip.Addr{}, err
	}
	return addresses[0].Unmap(), nil
}

func readNativeEgressTarget(connection net.Conn) (string, error) {
	var length [2]byte
	if _, err := io.ReadFull(connection, length[:]); err != nil {
		return "", err
	}
	size := int(binary.BigEndian.Uint16(length[:]))
	if size == 0 || size > 4096 {
		return "", fmt.Errorf("invalid egress target length %d", size)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(connection, data); err != nil {
		return "", err
	}
	return string(data), nil
}

func (p *nativeEgress) Close() error {
	p.cancel()
	_ = p.listener.Close()
	p.mu.Lock()
	for connection := range p.conns {
		_ = connection.Close()
	}
	p.mu.Unlock()
	<-p.done
	return nil
}
