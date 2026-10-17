package joincmd

import "sync"

type stringCollator interface {
	Compare(a, b string) (int, error)
	Close() error
}

type collatorOpener func(string) (stringCollator, error)

type collatorAdapter struct {
	provider stringCollator
	mu       sync.Mutex
	err      error
}

func newCollatorAdapter(provider stringCollator) *collatorAdapter {
	return &collatorAdapter{provider: provider}
}

func (c *collatorAdapter) Compare(a, b string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return 0
	}
	d, err := c.provider.Compare(a, b)
	if err != nil {
		c.err = err
		return 0
	}
	return d
}

func (c *collatorAdapter) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *collatorAdapter) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.provider.Close()
}
