package vm

import (
	"context"
	"encoding/binary"
	"errors"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/replay"
)

const (
	readStatusData = 0
	readStatusEOF  = 1
	readStatusFail = 2

	readStatusWouldBlock = 3
)

func (k *Kernel) readViaJournal(ctx context.Context, pid proc.PID, id uint32, buffer []byte, read func() (int, error)) (int, error) {
	journal := k.journal()
	if journal == nil {
		return read()
	}
	request := encodeReadRequest(id, len(buffer))
	output, err := replay.Run(ctx, journal, uint64(pid), replay.OpStdin, request,
		func(context.Context) ([]byte, string, error) {
			n, readErr := read()
			if readErr != nil {
				if errors.Is(readErr, errno.WouldBlock) {
					return []byte{readStatusWouldBlock}, "", nil
				}
				return append([]byte{readStatusFail}, []byte(readErr.Error())...), "", nil
			}
			if n == 0 {
				return []byte{readStatusEOF}, "", nil
			}
			return append([]byte{readStatusData}, buffer[:n]...), "", nil
		})
	if err != nil {
		return 0, err
	}
	if len(output) == 0 {
		return 0, errno.Invalid
	}
	switch output[0] {
	case readStatusData:

		return copy(buffer, output[1:]), nil
	case readStatusWouldBlock:
		return 0, errno.WouldBlock
	case readStatusEOF:
		return 0, errno.NotFound
	case readStatusFail:

		return 0, errno.Wrap(errno.Invalid, "回放的读取失败: "+string(output[1:]))
	default:
		return 0, errno.Invalid
	}
}

func encodeReadRequest(id uint32, length int) []byte {
	request := make([]byte, 6)
	binary.BigEndian.PutUint16(request[0:2], uint16(id))
	binary.BigEndian.PutUint32(request[2:6], uint32(length))
	return request
}

func (k *Kernel) journal() *replay.Memory {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.services.Journal
}
