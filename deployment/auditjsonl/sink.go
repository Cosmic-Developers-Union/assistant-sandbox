package auditjsonl

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/audit"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

const FormatVersion = 1

type Record struct {
	Version  int    `json:"version"`
	Sequence uint64 `json:"sequence"`
	PID      uint64 `json:"pid"`
	Kind     string `json:"kind"`

	Payload string `json:"payload,omitempty"`

	Redacted bool `json:"redacted,omitempty"`
}

type Redact func(Record) (Record, bool)

type Sink struct {
	mu       sync.Mutex
	writer   *bufio.Writer
	closer   io.Closer
	redact   Redact
	failure  error
	written  int
	closed   bool
	expected uint64
}

func New(writer io.Writer, closer io.Closer, redact Redact) (*Sink, error) {
	if writer == nil {
		return nil, errno.Invalid
	}
	return &Sink{writer: bufio.NewWriterSize(writer, 1<<12), closer: closer, redact: redact}, nil
}

func (s *Sink) Append(ctx context.Context, event audit.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errno.Cancelled
	}

	if s.failure != nil {
		return s.failure
	}

	if event.Sequence == 0 || event.Sequence <= s.expected {
		s.failure = fmt.Errorf("审计序号不递增: 收到 %d，上一条为 %d: %w", event.Sequence, s.expected, errno.Invalid)
		return s.failure
	}
	record := Record{
		Version:  FormatVersion,
		Sequence: event.Sequence,
		PID:      event.PID,
		Kind:     event.Kind,
		Payload:  string(event.Payload),
	}
	if s.redact != nil {
		filtered, keep := s.redact(record)
		if !keep {

			s.expected = event.Sequence
			return nil
		}
		filtered.Version = FormatVersion
		filtered.Sequence = event.Sequence
		filtered.PID = event.PID
		filtered.Kind = event.Kind
		filtered.Redacted = true
		record = filtered
	}
	encoded, err := json.Marshal(record)
	if err == nil {
		encoded = append(encoded, '\n')
		var written int
		written, err = s.writer.Write(encoded)
		if err == nil && written != len(encoded) {
			err = io.ErrShortWrite
		}
	}
	if err == nil {

		err = s.writer.Flush()
	}
	if err != nil {
		s.failure = fmt.Errorf("写入审计轨迹: %w", err)
		return s.failure
	}
	s.expected = event.Sequence
	s.written++
	return nil
}

func (s *Sink) Written() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.written
}

func (s *Sink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.failure
	}
	s.closed = true
	flushErr := s.writer.Flush()
	var closeErr error
	if s.closer != nil {
		closeErr = s.closer.Close()
	}
	s.failure = errors.Join(s.failure, flushErr, closeErr)
	return s.failure
}
