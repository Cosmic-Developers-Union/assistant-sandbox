package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/language"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
	"mvdan.cc/sh/v3/interp"
)

const LanguageID = "bash"

type slotState struct {
	Dir string            `json:"dir"`
	Env map[string]string `json:"env,omitempty"`
}

type Slot struct {
	mu      sync.Mutex
	kernel  *vm.Kernel
	parent  proc.Proc
	session *session
	sink    *mutableSink
	dir     string
	env     []string

	closed bool
}

func NewSlot(kernel *vm.Kernel, parent proc.Proc, dir string, environment []string) (*Slot, error) {
	if kernel == nil {
		return nil, errno.Wrap(errno.Invalid, "缺少内核")
	}

	sink := &mutableSink{}
	session, err := newSessionWithKernel(kernel, parent, dir, environment, nil, sink, sink)
	if err != nil {
		return nil, err
	}
	return &Slot{
		kernel:  kernel,
		parent:  parent,
		session: session,
		sink:    sink,
		dir:     dir,
		env:     append([]string(nil), environment...),
	}, nil
}

func (s *Slot) Language() string { return LanguageID }

const stepBaseCost = 1

func (s *Slot) Step(ctx context.Context, source []byte) (language.Output, error) {
	if err := ctx.Err(); err != nil {
		return language.Output{}, err
	}

	if err := s.charge(ctx, len(source)); err != nil {
		return language.Output{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return language.Output{}, errno.Wrap(errno.Cancelled, "槽位已关闭")
	}
	if s.session == nil {
		return language.Output{}, errno.Wrap(errno.Busy, "槽位已结束")
	}

	var buffer bytes.Buffer
	s.sink.setTarget(&buffer)
	defer s.sink.setTarget(nil)
	tree, err := parse(string(source))
	if err != nil {

		return language.Output{Data: []byte(err.Error()), Status: 2, Done: false}, nil
	}
	runErr := s.session.runner.Run(proc.WithContext(ctx, s.parent), tree)
	status, done := exitStatus(runErr)
	s.dir = s.session.runner.Dir

	_ = s.kernel.RunDueJobs(ctx)
	return language.Output{Data: append([]byte(nil), buffer.Bytes()...), Status: status, Done: done}, nil
}

func exitStatus(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	if status, ok := errors.AsType[interp.ExitStatus](err); ok {
		return int(status), false
	}
	return 1, false
}

func (s *Slot) charge(ctx context.Context, size int) error {
	meter, err := s.parent.Resources.Meter()
	if err != nil {
		if errors.Is(err, errno.NotImplemented) {
			return nil
		}
		return err
	}
	cost := uint64(size) + stepBaseCost
	return meter.ChargeKind(ctx, uint64(s.parent.ID), resources.KindWork, cost)
}

func (s *Slot) Encode(ctx context.Context) (language.State, error) {
	if err := ctx.Err(); err != nil {
		return language.State{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return language.State{}, errno.Wrap(errno.Cancelled, "槽位已关闭")
	}
	payload, err := json.Marshal(slotState{Dir: s.dir, Env: environMap(s.env)})
	if err != nil {
		return language.State{}, errno.Wrap(err, "编码 Bash 槽位状态")
	}
	encoded, err := language.State{Language: LanguageID, Payload: payload}.Encode()
	if err != nil {
		return language.State{}, err
	}
	return language.Decode(encoded)
}

func (s *Slot) Restore(ctx context.Context, state language.State) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errno.Wrap(errno.Cancelled, "槽位已关闭")
	}
	if state.Language != LanguageID {
		return errno.Wrap(errno.Invalid, "状态语言与槽位不符")
	}
	var restored slotState
	if err := json.Unmarshal(state.Payload, &restored); err != nil {
		return errno.Wrap(err, "解析 Bash 槽位状态")
	}
	if restored.Dir != "" {
		s.dir = restored.Dir
		if s.session != nil {
			s.session.runner.Dir = restored.Dir
		}
	}
	if restored.Env != nil {
		s.env = environList(restored.Env)
	}
	return nil
}

func (s *Slot) Close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.session != nil {
		s.session.close()
		s.session = nil
	}
	return nil
}

func environMap(environ []string) map[string]string {
	out := make(map[string]string, len(environ))
	for _, entry := range environ {
		for index := 0; index < len(entry); index++ {
			if entry[index] == '=' {
				out[entry[:index]] = entry[index+1:]
				break
			}
		}
	}
	return out
}

func environList(environ map[string]string) []string {
	names := make([]string, 0, len(environ))
	for name := range environ {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, name+"="+environ[name])
	}
	return out
}
