package cache

import (
	"errors"
	"time"

	"github.com/plgd-dev/go-coap/v3/pkg/sync"
	"go.uber.org/atomic"
)

var ErrCacheFull = errors.New("cache is full")

func DefaultOnExpire[D any](D) {
	// for nothing on expire
}

type Element[D any] struct {
	ValidUntil atomic.Time
	data       D
	onExpire   func(d D)
}

func (e *Element[D]) IsExpired(now time.Time) bool {
	value := e.ValidUntil.Load()
	if value.IsZero() {
		return false
	}
	return now.After(value)
}

func (e *Element[D]) Data() D {
	return e.data
}

func NewElement[D any](data D, validUntil time.Time, onExpire func(d D)) *Element[D] {
	if onExpire == nil {
		onExpire = DefaultOnExpire[D]
	}
	e := &Element[D]{data: data, onExpire: onExpire}
	e.ValidUntil.Store(validUntil)
	return e
}

type Cache[K comparable, D any] struct {
	*sync.Map[K, *Element[D]]
}

func NewCache[K comparable, D any]() *Cache[K, D] {
	return &Cache[K, D]{
		Map: sync.NewMap[K, *Element[D]](),
	}
}

func (c *Cache[K, D]) LoadOrStore(key K, e *Element[D]) (actual *Element[D], loaded bool) {
	now := time.Now()
	c.ReplaceWithFunc(key, func(oldValue *Element[D], oldLoaded bool) (newValue *Element[D], deleteValue bool) {
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

// LoadOrStoreWithLimit loads an unexpired entry or atomically stores e if there
// is capacity. A non-positive maxEntries means unlimited. Expired entries are
// reclaimed when the cache is full. Existing entries can be loaded at capacity.
func (c *Cache[K, D]) LoadOrStoreWithLimit(key K, e *Element[D], maxEntries int) (actual *Element[D], loaded bool, err error) {
	now := time.Now()
	if maxEntries > 0 && c.Length() >= maxEntries {
		c.CheckExpirations(now)
	}
	var expired *Element[D]
	actual, _, accepted := c.LoadOrStoreWithFuncAndLimit(key, maxEntries, func(old *Element[D]) *Element[D] {
		if old.IsExpired(now) {
			expired = old
			return e
		}
		return old
	}, func() *Element[D] {
		return e
	})
	if expired != nil {
		expired.onExpire(expired.Data())
	}
	if !accepted {
		return nil, false, ErrCacheFull
	}
	return actual, actual != e, nil
}

func (c *Cache[K, D]) Load(key K) (actual *Element[D]) {
	actual, loaded := c.Map.Load(key)
	if !loaded {
		return nil
	}
	if actual.IsExpired(time.Now()) {
		return nil
	}
	return actual
}

func (c *Cache[K, D]) CheckExpirations(now time.Time) {
	c.Range(func(key K, value *Element[D]) bool {
		if value.IsExpired(now) {
			removed := false
			c.ReplaceWithFunc(key, func(current *Element[D], loaded bool) (*Element[D], bool) {
				removed = loaded && current == value && current.IsExpired(now)
				return current, !loaded || removed
			})
			if removed {
				value.onExpire(value.Data())
			}
		}
		return true
	})
}
