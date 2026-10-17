package entropy

import (
	"context"
	"sync"
)

type Counter struct {
	mu   sync.Mutex
	next uint64
}

func (c *Counter) Read(ctx context.Context, p []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range p {
		c.next++
		p[i] = byte(c.next ^ (c.next >> 8))
	}
	return len(p), nil
}
