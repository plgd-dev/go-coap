package cache

import (
	"time"

	"github.com/plgd-dev/go-coap/v3/pkg/sync"
)

func DefaultOnExpire[D any](d D) {
	// for nothing on expire
}

type Element[D any] struct {
	validUntil time.Time
	data       D
	onExpire   func(d D)
}

func (e *Element[D]) IsExpired(now time.Time) bool {
	if e.validUntil.IsZero() {
		return false
	}
	return now.After(e.validUntil)
}

func (e *Element[D]) Data() D {
	return e.data
}

func NewElement[D any](data D, validUntil time.Time, onExpire func(d D)) *Element[D] {
	if onExpire == nil {
		onExpire = DefaultOnExpire[D]
	}
	return &Element[D]{data: data, validUntil: validUntil, onExpire: onExpire}
}

type Cache[K comparable, D any] struct {
	data *sync.Map[K, *Element[D]]
}

func NewCache[K comparable, D any]() *Cache[K, D] {
	return &Cache[K, D]{
		data: sync.NewMap[K, *Element[D]](),
	}
}

func (c *Cache[K, D]) LoadOrStore(key K, e *Element[D]) (actual *Element[D], loaded bool) {
	now := time.Now()
	c.data.ReplaceWithFunc(key, func(oldValue *Element[D], oldLoaded bool) (newValue *Element[D], deleteValue bool) {
		if oldLoaded {
			if !oldValue.IsExpired(now) {
				actual = oldValue
				return oldValue, false
			}
		}
		actual = e
		return e, false
	})
	return actual, actual != e
}

func (c *Cache[K, D]) Load(key K) (actual *Element[D]) {
	actual, loaded := c.data.Load(key)
	if !loaded {
		return nil
	}
	if actual.IsExpired(time.Now()) {
		return nil
	}
	return actual
}

func (c *Cache[K, D]) Delete(key K) {
	c.data.Delete(key)
}

func (c *Cache[K, D]) CheckExpirations(now time.Time) {
	for k, e := range c.data.CopyData() {
		if e.IsExpired(now) {
			c.data.Delete(k)
			e.onExpire(e.data)
		}
	}
}

func (c *Cache[K, D]) PullOutAll() map[K]*Element[D] {
	return c.data.PullOutAll()
}
