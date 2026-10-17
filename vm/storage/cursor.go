package storage

import (
	"context"
	"math"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type replayCursor interface {
	Position() int

	Total() int

	Recording() bool
}

func (e *Envelope) ReconcileCursor(ctx context.Context, journal replayCursor) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e == nil {
		return errno.Invalid
	}
	if journal == nil {
		return nil
	}
	if e.Format != EnvelopeFormat {
		return errno.Wrap(errno.Version, "快照信封格式为 "+e.Format)
	}
	if journal.Recording() {
		return errno.Wrap(errno.Invalid, "快照对账要求回放模式的事实流")
	}
	if e.CursorPos > math.MaxInt {
		return errno.Wrap(errno.Version, "快照游标超出可寻址范围")
	}
	cursor := int(e.CursorPos)
	if cursor > journal.Total() {
		return errno.Wrap(errno.Version, "快照游标超出事实流范围")
	}
	if journal.Position() > cursor {
		return errno.Wrap(errno.Version, "事实流位置领先于快照游标")
	}
	return nil
}
