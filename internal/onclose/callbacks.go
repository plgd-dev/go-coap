package onclose

import "sync"

// Callbacks invokes each registered callback once when shutdown begins.
// Callbacks registered after shutdown begins run immediately.
type Callbacks struct {
	mu        sync.Mutex
	closed    bool
	callbacks []func()
}

func (c *Callbacks) Add(f func()) {
	c.mu.Lock()
	if !c.closed {
		c.callbacks = append(c.callbacks, f)
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	f()
}

func (c *Callbacks) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	callbacks := c.callbacks
	c.callbacks = nil
	c.mu.Unlock()
	for _, f := range callbacks {
		f()
	}
}
