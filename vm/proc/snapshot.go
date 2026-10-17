package proc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	stdfs "io/fs"
	"maps"
	"slices"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

const TableFormat = "assistant-sandbox-proctable-1"

type tableSnapshot struct {
	Format  string          `json:"format"`
	Next    PID             `json:"next"`
	Init    PID             `json:"init"`
	Entries []processRecord `json:"entries"`
}

type processRecord struct {
	ID         PID               `json:"id"`
	Parent     PID               `json:"parent"`
	Generation uint64            `json:"generation"`
	Program    string            `json:"program"`
	Args       []string          `json:"args,omitempty"`
	Dir        string            `json:"dir"`
	Env        map[string]string `json:"env,omitempty"`
	State      State             `json:"state"`
	ExitCode   uint8             `json:"exit_code,omitempty"`
	ExitSignal uint8             `json:"exit_signal,omitempty"`
	PGID       PID               `json:"pgid"`
	SID        PID               `json:"sid"`
	Umask      stdfs.FileMode    `json:"umask"`
}

const maxSnapshotEntries = 1 << 20

func (t *Table) Snapshot(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t.init == 0 {

		return nil, errno.Busy
	}
	snapshot := tableSnapshot{
		Format:  TableFormat,
		Next:    t.next,
		Init:    t.init,
		Entries: make([]processRecord, 0, len(t.entries)),
	}
	for _, id := range slices.Sorted(maps.Keys(t.entries)) {
		p := t.entries[id]
		snapshot.Entries = append(snapshot.Entries, processRecord{
			ID:         p.ID,
			Parent:     p.Parent,
			Generation: p.Generation,
			Program:    p.Program,
			Args:       slices.Clone(p.Args),
			Dir:        p.Dir,
			Env:        maps.Clone(p.Env),
			State:      p.State,
			ExitCode:   p.ExitCode,
			ExitSignal: p.ExitSignal,
			PGID:       p.PGID,
			SID:        p.SID,
			Umask:      p.Umask,
		})
	}
	return marshalTableSnapshot(snapshot)
}

func marshalTableSnapshot(snapshot tableSnapshot) ([]byte, error) {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil, errno.Wrap(err, "编码进程表快照")
	}
	return encoded, nil
}

func (t *Table) Restore(ctx context.Context, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(data) == 0 {
		return errno.Invalid
	}
	snapshot, err := decodeTableSnapshot(data)
	if err != nil {
		return err
	}
	entries, next, err := snapshot.materialize()
	if err != nil {
		return err
	}
	t.entries = entries
	t.next = next
	t.init = snapshot.Init
	return nil
}

func decodeTableSnapshot(data []byte) (tableSnapshot, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var snapshot tableSnapshot
	if err := decoder.Decode(&snapshot); err != nil {

		return tableSnapshot{}, errno.Wrap(errno.Invalid, "解析进程表快照: "+err.Error())
	}
	if snapshot.Format != TableFormat {
		return tableSnapshot{}, errno.Wrap(errno.Version, "进程表快照格式为 "+snapshot.Format)
	}
	if len(snapshot.Entries) == 0 || len(snapshot.Entries) > maxSnapshotEntries {
		return tableSnapshot{}, errno.Wrap(errno.Invalid, "进程表快照条目数非法")
	}
	return snapshot, nil
}

func (s tableSnapshot) materialize() (map[PID]Proc, PID, error) {
	entries := make(map[PID]Proc, len(s.Entries))
	maxID := PID(0)
	for _, record := range s.Entries {
		if record.ID == 0 {
			return nil, 0, errno.Wrap(errno.Invalid, "进程表快照含 0 号进程")
		}
		if _, duplicate := entries[record.ID]; duplicate {
			return nil, 0, errno.Wrap(errno.Invalid, fmt.Sprintf("进程表快照重复 PID %d", record.ID))
		}
		if record.Program == "" || record.Dir == "" || record.Dir[0] != '/' {
			return nil, 0, errno.Wrap(errno.Invalid, fmt.Sprintf("进程 %d 缺少程序名或绝对目录", record.ID))
		}
		if record.Generation == 0 {
			return nil, 0, errno.Wrap(errno.Invalid, fmt.Sprintf("进程 %d 代数非法", record.ID))
		}
		if record.State > Zombie {
			return nil, 0, errno.Wrap(errno.Invalid, fmt.Sprintf("进程 %d 状态非法", record.ID))
		}
		if record.ID > maxID {
			maxID = record.ID
		}
		entries[record.ID] = Proc{
			ID:         record.ID,
			Parent:     record.Parent,
			Generation: record.Generation,
			Program:    record.Program,
			Args:       slices.Clone(record.Args),
			Dir:        record.Dir,
			Env:        cloneEnv(record.Env),
			State:      record.State,
			ExitCode:   record.ExitCode,
			ExitSignal: record.ExitSignal,
			PGID:       record.PGID,
			SID:        record.SID,
			Umask:      record.Umask,
		}
	}
	initProcess, ok := entries[s.Init]
	if !ok || initProcess.Parent != 0 {
		return nil, 0, errno.Wrap(errno.Invalid, "进程表快照的启动进程不合法")
	}

	for id, p := range entries {
		if p.Parent == 0 && id != s.Init {
			return nil, 0, errno.Wrap(errno.Invalid, fmt.Sprintf("进程 %d 不应是根进程", id))
		}
		if p.Parent != 0 {
			if _, present := entries[p.Parent]; !present {

				p.Parent = s.Init
				entries[id] = p
			}
		}
		if p.Umask&^stdfs.ModePerm != 0 {
			return nil, 0, errno.Wrap(errno.Invalid, fmt.Sprintf("进程 %d 的创建掩码只能是权限位", id))
		}
		if p.PGID == 0 || p.SID == 0 {
			return nil, 0, errno.Wrap(errno.Invalid, fmt.Sprintf("进程 %d 缺少进程组或会话", id))
		}
	}

	next := max(s.Next, maxID+1)
	if next == 0 {
		return nil, 0, errno.Wrap(errno.Overflow, "进程表 PID 计数溢出")
	}
	return entries, next, nil
}

func cloneEnv(env map[string]string) map[string]string {
	if env == nil {
		return nil
	}
	cloned := make(map[string]string, len(env))
	for name, value := range env {
		cloned[name] = value
	}
	return cloned
}
