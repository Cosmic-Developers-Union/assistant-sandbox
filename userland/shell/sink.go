package shell

import (
	"bytes"
	"io"
	"sync"
)

type mutableSink struct {
	mu     sync.Mutex
	target io.Writer
}

func (s *mutableSink) setTarget(target io.Writer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.target = target
}

func (s *mutableSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	target := s.target
	s.mu.Unlock()
	if target == nil {
		return len(p), nil
	}
	return target.Write(p)
}

func (s *mutableSink) bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if buffer, ok := s.target.(*bytes.Buffer); ok {
		return append([]byte(nil), buffer.Bytes()...)
	}
	return nil
}
