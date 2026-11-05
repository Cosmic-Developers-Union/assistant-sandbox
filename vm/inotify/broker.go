package inotify

import "sync"

// Observer 是事件发射端口(I20):系统调用层在成功的文件系统操作
// 后调用 Observe,path 为受影响条目的客体绝对路径,mask 为纯事件位
// (不含 InIsdir 等内核位),cookie 配对 rename 双事件。NextCookie
// 供 renameat 生成配对 cookie;Register/Unregister 管理 inotify
// 实例的扇出登记。Broker 实现本接口。
type Observer interface {
	Observe(path string, mask uint32, cookie uint32, isDir bool) bool

	NextCookie() uint32

	Register(fd *FD)

	Unregister(fd *FD)
}

// Broker 是跨进程的事件扇出注册表:系统调用层在成功的文件系统操作
// 后调用 Observe,事件送达所有存活实例的命中监视。已关闭的实例在
// 扇出时惰性剔除。
type Broker struct {
	mu   sync.Mutex
	set  map[*FD]struct{}
	next uint32
}

// NewBroker 创建空的扇出注册表。
func NewBroker() *Broker {
	return &Broker{set: map[*FD]struct{}{}}
}

// Register 登记一个 inotify 实例。
func (b *Broker) Register(fd *FD) {
	b.mu.Lock()
	b.set[fd] = struct{}{}
	b.mu.Unlock()
}

// Unregister 移除一个 inotify 实例(显式关闭路径)。
func (b *Broker) Unregister(fd *FD) {
	b.mu.Lock()
	delete(b.set, fd)
	b.mu.Unlock()
}

// NextCookie 返回下一个 rename 配对 cookie。
func (b *Broker) NextCookie() uint32 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	return b.next
}

// Observe 向全部存活实例扇出一次事件;返回是否有实例收到事件。
func (b *Broker) Observe(path string, mask uint32, cookie uint32, isDir bool) bool {
	b.mu.Lock()
	instance := make([]*FD, 0, len(b.set))
	for fd := range b.set {
		if fd.Closed() {
			delete(b.set, fd)
			continue
		}
		instance = append(instance, fd)
	}
	b.mu.Unlock()
	grew := false
	for _, fd := range instance {
		if fd.Observe(path, mask, cookie, isDir) {
			grew = true
		}
	}
	return grew
}

// Closed 报告实例是否已关闭(惰性剔除判定)。
func (f *FD) Closed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}
