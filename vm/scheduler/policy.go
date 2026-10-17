package scheduler

import (
	"sort"
	"strconv"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

func SpawnDetail(program string) string { return program }

func ExitDetail(code uint8, number signal.Number) string {
	return "code=" + itoa(uint64(code)) + " signal=" + SignalDetail(number)
}

func SignalDetail(number signal.Number) string { return number.String() }

func ReapDetail(program string) string { return program }

func ExecDetail(program string, generation uint64) string {
	return "program=" + program + " generation=" + itoa(generation)
}

func SnapshotDetail(bytes int) string { return "bytes=" + strconv.Itoa(bytes) }

func RestoreDetail(bytes int) string { return "bytes=" + strconv.Itoa(bytes) }

func itoa(value uint64) string {
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[index:])
}

type Task struct {
	PID uint64

	SpawnTick uint64

	Program string

	Cost uint64

	Exclusive bool

	Nice int
}

type Step struct {
	Tick uint64

	PID uint64

	Remaining uint64

	Preempted bool
}

type Policy struct {
	Quantum uint64

	Preemptible bool
}

func planError(reason string) error { return errno.Wrap(errno.Invalid, reason) }

func (p Policy) Plan(tasks []Task) ([]Step, error) {
	if len(tasks) == 0 {
		return nil, planError("调度计划至少需要一个任务")
	}
	pending := make([]Task, len(tasks))
	copy(pending, tasks)
	for _, task := range pending {
		if task.PID == 0 {
			return nil, planError("任务 PID 不能为 0")
		}
	}

	sort.SliceStable(pending, func(i, j int) bool {
		if pending[i].Nice != pending[j].Nice {
			return pending[i].Nice < pending[j].Nice
		}
		if pending[i].SpawnTick != pending[j].SpawnTick {
			return pending[i].SpawnTick < pending[j].SpawnTick
		}
		return pending[i].PID < pending[j].PID
	})

	remaining := make([]uint64, len(pending))
	for index, task := range pending {
		remaining[index] = task.Cost
	}
	steps := make([]Step, 0, len(pending))
	tick := pending[0].SpawnTick
	for {
		progress := false
		for index := range pending {
			if remaining[index] == 0 {
				continue
			}
			progress = true
			slice := p.slice(pending[index], remaining[index])
			remaining[index] -= slice
			steps = append(steps, Step{
				Tick:      tick,
				PID:       pending[index].PID,
				Remaining: remaining[index],
				Preempted: remaining[index] > 0,
			})

			if remaining[index] == 0 {
				tick++
			}
		}
		if !progress {
			break
		}

		allDone := true
		for _, left := range remaining {
			if left > 0 {
				allDone = false
			}
		}
		if allDone {
			break
		}
		if !p.Preemptible {

			for index := range pending {
				if remaining[index] > 0 {
					return nil, planError("不可抢占策略下任务未跑完")
				}
			}
			break
		}
	}
	return steps, nil
}

func (p Policy) slice(task Task, remaining uint64) uint64 {
	if p.Quantum == 0 || task.Exclusive || !p.Preemptible {
		return remaining
	}
	if p.Quantum < remaining {
		return p.Quantum
	}
	return remaining
}

type Outcome struct {
	Code   uint8
	Signal signal.Number
}

func (s *Sequencer) AppendPlan(tasks []Task, steps []Step, outcomes map[uint64]Outcome) {
	byPID := make(map[uint64]Task, len(tasks))
	for _, task := range tasks {
		byPID[task.PID] = task
	}
	spawned := make(map[uint64]bool, len(tasks))
	for _, step := range steps {
		task := byPID[step.PID]
		if !spawned[step.PID] {
			if s.Append(step.PID, KindSpawn, SpawnDetail(task.Program)) == 0 {
				return
			}
			spawned[step.PID] = true
		}
		if step.Remaining == 0 {
			outcome := outcomes[step.PID]
			if s.Append(step.PID, KindExit, ExitDetail(outcome.Code, outcome.Signal)) == 0 {
				return
			}
		}
	}
}
