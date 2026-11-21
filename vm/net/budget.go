package net

import (
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// Budget 是每台 VM 的网络资源预算(M12):连接数与累计流量。
// 零值表示不限制;连接关闭或命名空间关闭时归还连接额度。
type Budget struct {
	MaxConnections int
	MaxBytes       uint64
}

// Usage 是当前网络资源使用量。
type Usage struct {
	Connections int
	Bytes       uint64
}

type budgetState struct {
	mu          sync.Mutex
	limits      Budget
	connections int
	bytes       uint64
}

func (b *budgetState) chargeConnection() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limits.MaxConnections > 0 && b.connections >= b.limits.MaxConnections {
		return errno.Wrap(errno.Limit, "网络连接预算耗尽")
	}
	b.connections++
	return nil
}

func (b *budgetState) releaseConnection() {
	b.mu.Lock()
	if b.connections > 0 {
		b.connections--
	}
	b.mu.Unlock()
}

func (b *budgetState) chargeBytes(count int) error {
	if count <= 0 {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	next := b.bytes + uint64(count)
	if b.limits.MaxBytes > 0 && next > b.limits.MaxBytes {
		return errno.Wrap(errno.NoSpace, "网络流量预算耗尽")
	}
	b.bytes = next
	return nil
}

func (b *budgetState) usage() Usage {
	b.mu.Lock()
	defer b.mu.Unlock()
	return Usage{Connections: b.connections, Bytes: b.bytes}
}

// SetBudget 装配网络预算(M12);nil 或零值表示不限制。已在途连接
// 不会被强制拆除,后续新建/发送受新预算约束。
func (n *Namespace) SetBudget(budget Budget) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.budget.limits = budget
}

// Usage 返回当前网络使用量(连接数与累计发送字节)。
func (n *Namespace) Usage() Usage { return n.budget.usage() }

func (n *Namespace) chargeConnection() error { return n.budget.chargeConnection() }

func (n *Namespace) trackConns(conns ...*streamConn) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.conns == nil {
		n.conns = map[*streamConn]struct{}{}
	}
	for _, conn := range conns {
		n.conns[conn] = struct{}{}
	}
}
func (n *Namespace) releaseConnection() { n.budget.releaseConnection() }
func (n *Namespace) chargeBytes(count int) error {
	return n.budget.chargeBytes(count)
}
