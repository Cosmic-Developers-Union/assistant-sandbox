package sandbox

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
)

const maxMessagesBytes = 8 << 20

type MessageSource func(context.Context) (string, []jsontext.Value, error)

func messageSnapshot(ctx context.Context, source MessageSource) (string, []jsontext.Value, error) {
	if source == nil {
		return "", nil, nil
	}
	latest, records, err := source(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("读取消息快照: %w", err)
	}
	size := len(latest)
	for _, record := range records {
		size += len(record)
	}
	if size > maxMessagesBytes {
		return "", nil, fmt.Errorf("完整消息快照超过 8 MiB，请由宿主缩小公开范围")
	}
	return latest, records, nil
}
