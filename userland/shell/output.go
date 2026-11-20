package shell

import "sync"

// OutputSource 标识一段输出的来源(P06):前台命令或后台任务。
type OutputSource string

const (
	OutputForeground OutputSource = "foreground"
	OutputBackground OutputSource = "background"
)

// OutputChunk 是带序列边界的输出片段。
type OutputChunk struct {
	Sequence uint64
	Source   OutputSource
	Data     []byte
}

// outputLog 是 Console 会话级的有界输出历史:即使没有连接,前后台
// 输出仍按写入顺序追加,调用方用 Sequence 做增量读取(P06)。
type outputLog struct {
	mu     sync.Mutex
	limit  int
	next   uint64
	chunks []OutputChunk
}

func newOutputLog(limit int) *outputLog {
	if limit <= 0 {
		limit = 4096
	}
	return &outputLog{limit: limit, next: 1}
}

func (l *outputLog) record(source OutputSource, data []byte) {
	if l == nil || len(data) == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.chunks = append(l.chunks, OutputChunk{
		Sequence: l.next,
		Source:   source,
		Data:     append([]byte(nil), data...),
	})
	l.next++
	if len(l.chunks) > l.limit {
		l.chunks = l.chunks[len(l.chunks)-l.limit:]
	}
}

// since 返回序号大于 sequence 的片段副本;sequence=0 表示从头读取。
func (l *outputLog) since(sequence uint64) []OutputChunk {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]OutputChunk, 0, len(l.chunks))
	for _, chunk := range l.chunks {
		if chunk.Sequence <= sequence {
			continue
		}
		cloned := chunk
		cloned.Data = append([]byte(nil), chunk.Data...)
		out = append(out, cloned)
	}
	return out
}
