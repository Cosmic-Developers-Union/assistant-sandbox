package shell

import (
	"bytes"
	"io"
	"sync"
)

type mutableSink struct {
	mu     sync.Mutex
	target io.Writer

	// persistent 是持续输出目标:后台进程在命令返回后仍写入的输出
	// 也经此送往控制台屏幕(P10)。
	persistent io.Writer

	// observer 记录来源与序列边界(P06):前台 Write 与后台
	// persistentWriter 分别上报。
	observer func(OutputSource, []byte)
}

// setObserver 登记输出历史记录器。
func (s *mutableSink) setObserver(observer func(OutputSource, []byte)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observer = observer
}

func (s *mutableSink) notify(source OutputSource, p []byte) {
	s.mu.Lock()
	observer := s.observer
	s.mu.Unlock()
	if observer != nil {
		observer(source, p)
	}
}

// setPersistent 登记持续输出目标。
func (s *mutableSink) setPersistent(target io.Writer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persistent = target
}

func (s *mutableSink) setTarget(target io.Writer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.target = target
}

func (s *mutableSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	target := s.target
	persistent := s.persistent
	s.mu.Unlock()
	if target != nil {
		if _, err := target.Write(p); err != nil {
			return 0, err
		}
	}
	if persistent != nil {
		if _, err := persistent.Write(p); err != nil {
			return 0, err
		}
	}
	s.notify(OutputForeground, p)
	return len(p), nil
}

// persistentWriter 只写持续输出目标:后台任务的输出不进入当前步骤
// 的捕获缓冲(前台命令的输出不受后台任务影响)。
func (s *mutableSink) persistentWriter() io.Writer { return persistentWriter{sink: s} }

// persistentWriter 是只写持续输出目标的写入器。
type persistentWriter struct {
	sink *mutableSink
}

func (w persistentWriter) Write(p []byte) (int, error) {
	w.sink.mu.Lock()
	target := w.sink.persistent
	w.sink.mu.Unlock()
	if target != nil {
		n, err := target.Write(p)
		if err != nil {
			return n, err
		}
	}
	w.sink.notify(OutputBackground, p)
	return len(p), nil
}

func (s *mutableSink) bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if buffer, ok := s.target.(*bytes.Buffer); ok {
		return append([]byte(nil), buffer.Bytes()...)
	}
	return nil
}
