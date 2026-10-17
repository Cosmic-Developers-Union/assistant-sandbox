package replay

import (
	"fmt"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type positioned interface {
	Position() int
}

type MismatchError struct {
	Position int
	Expected Fact
	Actual   Fact
	Reason   string
}

func (e *MismatchError) Error() string {
	return fmt.Sprintf("回放在第 %d 条事实分歧（%s）：期待 %s@pid%d 输入 %x，实际 %s@pid%d 输入 %x",
		e.Position+1, e.Reason, e.Expected.Operation, e.Expected.PID, e.Expected.Input,
		e.Actual.Operation, e.Actual.PID, e.Actual.Input)
}

func (e *MismatchError) Unwrap() error { return errno.Invalid }

func errnoRequire(journal Journal) (bool, error) {
	if journal == nil {
		return false, errno.Invalid
	}
	return true, nil
}

func (m *Memory) Recording() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.record
}
