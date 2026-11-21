package capability

import (
	stdfs "io/fs"
	"reflect"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/audit"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/bridge"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/image"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/ipc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/language"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/net"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/replay"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/storage"
)

type Access interface {
	Filesystem() (fs.System, error)
	Time() (clock.Source, error)
	Random() (entropy.Source, error)
	Credentials() (identity.Source, error)
	Descriptors() (fd.Descriptors, error)

	AddressSpace() (Spaces, error)
	Meter() (resources.Meter, error)

	Umask() (stdfs.FileMode, error)
	Host() (bridge.Bridge, error)
	InitialImage() (image.Image, error)
	Events() (audit.Sink, error)
	Languages() (language.Slots, error)
	SessionStore() (storage.Sessions, error)
	SnapshotStore() (storage.Snapshots, error)
	Journal() (replay.Journal, error)
	Network() (net.Network, error)
	Communication() (ipc.IPC, error)
}

type Set struct {
	FS       fs.System
	Clock    clock.Source
	Entropy  entropy.Source
	Identity identity.Source
	FD       fd.Descriptors

	Spaces Spaces

	UmaskSource UmaskSource
	Resources   resources.Meter
	Bridge      bridge.Bridge
	Image       image.Image
	Audit       audit.Sink
	Language    language.Slots
	Sessions    storage.Sessions
	Snapshots   storage.Snapshots
	Replay      replay.Journal
	Net         net.Network

	// Egress 是宿主 TCP 出口的默认拒绝策略(M09);与 Net 同时装配。
	Egress *net.EgressPolicy

	IPC ipc.IPC
}

type UmaskSource interface {
	Umask(pid uint64) (stdfs.FileMode, error)
}

func Require[T any](value T) (T, error) {
	v := reflect.ValueOf(value)
	if !v.IsValid() {
		var zero T
		return zero, errno.NotImplemented
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		if v.IsNil() {
			var zero T
			return zero, errno.NotImplemented
		}
	}
	return value, nil
}

func (s Set) Filesystem() (fs.System, error) { return Require(s.FS) }

func (s Set) Time() (clock.Source, error) { return Require(s.Clock) }

func (s Set) Random() (entropy.Source, error) { return Require(s.Entropy) }

func (s Set) Credentials() (identity.Source, error) { return Require(s.Identity) }

func (s Set) Descriptors() (fd.Descriptors, error) { return Require(s.FD) }

func (s Set) AddressSpace() (Spaces, error) { return Require(s.Spaces) }

func (s Set) Meter() (resources.Meter, error) { return Require(s.Resources) }

func (s Set) Host() (bridge.Bridge, error) { return Require(s.Bridge) }

func (s Set) InitialImage() (image.Image, error) { return Require(s.Image) }

func (s Set) Events() (audit.Sink, error) { return Require(s.Audit) }

func (s Set) Languages() (language.Slots, error) { return Require(s.Language) }

func (s Set) SessionStore() (storage.Sessions, error) { return Require(s.Sessions) }

func (s Set) SnapshotStore() (storage.Snapshots, error) { return Require(s.Snapshots) }

func (s Set) Journal() (replay.Journal, error) { return Require(s.Replay) }

func (s Set) Network() (net.Network, error) { return Require(s.Net) }

func (s Set) Communication() (ipc.IPC, error) { return Require(s.IPC) }

func (s Set) Umask() (stdfs.FileMode, error) {
	if s.UmaskSource == nil {
		return 0, errno.Wrap(errno.NotImplemented, "装配层未提供创建掩码")
	}
	return s.UmaskSource.Umask(0)
}

type Spaces interface {
	BindAddressSpace(pid uint64, inner *memory.Table) (memory.Space, error)
}
