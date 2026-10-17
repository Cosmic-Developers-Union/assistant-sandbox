package proc

import (
	"context"
	stdfs "io/fs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

type contextKey struct{}

func WithContext(ctx context.Context, p Proc) context.Context {
	return context.WithValue(ctx, contextKey{}, p)
}

func FromContext(ctx context.Context) (Proc, error) {
	p, ok := ctx.Value(contextKey{}).(Proc)
	if !ok || p.Resources == nil {
		return Proc{}, errno.NotImplemented
	}
	return p, nil
}

type RunSpec struct {
	Parent  PID
	Name    string
	Args    []string
	Dir     string
	Env     map[string]string
	Streams fd.Streams

	IgnoreSignals []signal.Number

	NiceAdjust *int
}

type Control interface {
	Begin(PID, string, []string, string, map[string]string, fd.Streams) (Proc, error)
	Finish(PID, PID, uint8) error

	Run(context.Context, RunSpec) (Proc, error)
}

type Usage struct {
	Self uint64

	Children uint64
}

type UsageControl interface {
	Usage(PID) (Usage, error)
}

type TableControl interface {
	Processes() []Proc
}

type SignalControl interface {
	Signal(PID, signal.Number) error

	Exists(PID) (bool, error)
}

type UmaskControl interface {
	SetUmask(PID, stdfs.FileMode) error
}

type PriorityControl interface {
	Nice(PID) (int, error)

	SetNice(PID, int) error
}

type JobSpec struct {
	Queue    byte
	Deadline uint64
	Nice     int
	Dir      string
	Env      map[string]string
	Mail     bool
	Script   string
}

type PrintSpec struct {
	Destination string
	Copies      int
	Title       string

	Copy   bool
	Notify bool
	Write  bool
	Body   string
}

type Scheduler interface {
	QueueJob(context.Context, JobSpec) (string, error)

	ListJobs(context.Context) ([]string, error)

	RemoveJob(context.Context, string) (bool, error)

	ReadJob(context.Context, string) (string, error)

	QueuePrint(context.Context, PrintSpec) (string, error)

	ListPrintRequests(context.Context) ([]string, error)

	RunDueJobs(context.Context) error
}

const ArgMax = 128 << 10

type SystemConfiguration struct {
	BootTime int64

	BootDeclared bool

	ChildMax int64

	ArgMax int64
}

type SystemControl interface {
	SystemConfiguration() (SystemConfiguration, error)
}
