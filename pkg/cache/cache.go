package cache

import (
	"time"

	"github.com/plgd-dev/go-coap/v2/pkg/sync"
)

type Element[T any] struct {
	validUntil time.Time
	data       T
	onExpire   func(d T)
}

func newElement[T any](data T, validUntil time.Time, onExpire func(d T)) *Element[T] {
	if onExpire == nil {
		onExpire = func(d T) {
			// NO-OP as default
		}
	}
	return &Element[T]{data: data, validUntil: validUntil, onExpire: onExpire}
}

func (e *Element[T]) IsExpired(now time.Time) bool {
	if e.validUntil.IsZero() {
		return false
	}
	return now.After(e.validUntil)
}

func (e *Element[T]) Data() T {
	return e.data
}

type Cache[K comparable, V any] struct {
	data sync.Map[K, *Element[V]]
}

// NewCache creates a new synchronize map.
func NewCache[K comparable, V any]() *Cache[K, V] {
	return &Cache[K, V]{
		data: *sync.NewMap[K, *Element[V]](),
	}
}

// NewElement creates element that can be stored in the cache.
func (c *Cache[K, V]) NewElement(data V, validUntil time.Time, onExpire func(d V)) *Element[V] {
	return newElement(data, validUntil, onExpire)
}

// LoadOrStore loads or creates a new element for key.
//
// If an element for the key exists then this element (oldE) is returned
// and the loaded value is set to true. Thus a pair of (oldE, true) is returned.
// If no element for the key exists then a new elem (newE) is stored in the cache
// and the returned pair is (newE, false).
func (c *Cache[K, V]) LoadOrStore(key K, e *Element[V]) (actual *Element[V], loaded bool) {
	now := time.Now()
	c.data.ReplaceWithFunc(key, func(oldValue *Element[V], oldLoaded bool) (newValue *Element[V], deleteValue bool) {
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

// Load loads unexpired element with given key from cache.
//
// If an element for key is not found then (nil, false) is returned.
// If an element for key is found but the element is expired then (nil, true) is returned.
// If an unexpired element for key is found then (*Element, true) is returned.
func (c *Cache[K, V]) Load(key K) (element *Element[V], loaded bool) {
	a, loaded := c.data.Load(key)
	if !loaded {
		return nil, false
	}
	if a.IsExpired(time.Now()) {
		return nil, true
	}
	return a, true
}

// Delete removes the element for given key from the cache.
func (c *Cache[K, V]) Delete(key K) (deleted bool) {
	return c.data.Delete(key)
}

// CheckExpirations iterates over all elements in the cache, checks each for expiration,
// deletes expired elements from cache and invokes onExpire function on the element.
func (c *Cache[K, V]) CheckExpirations(now time.Time) {
	m := make(map[K]*Element[V])
	c.data.Range(func(key K, value *Element[V]) bool {
		m[key] = value
		return true
	})
	for k, e := range m {
		if e.IsExpired(now) {
			c.data.Delete(k)
			e.onExpire(e.data)
		}
	}
}

// PullOutAll removes all elements from the cache and returns them in a map.
func (c *Cache[K, V]) PullOutAll() map[K]V {
	res := make(map[K]V)
	for key, value := range c.data.PullOutAll() {
		res[key] = value.Data()
	}
	return res
}
