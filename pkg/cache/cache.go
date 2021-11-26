package cache

import (
	"time"

	kitSync "github.com/plgd-dev/kit/v2/sync"
)

func DefaultOnExpire(d interface{}) {
	// for nothing on expire
}

type Element struct {
	data       interface{}
	validUntil time.Time
	onExpire   func(d interface{})
}

func (e *Element) IsExpired(now time.Time) bool {
	if e.validUntil.IsZero() {
		return false
	}
	return now.After(e.validUntil)
}

func (e *Element) Data() interface{} {
	return e.data
}

func NewElement(data interface{}, validUntil time.Time, onExpire func(d interface{})) *Element {
	if onExpire == nil {
		onExpire = defaultOnExpire
	}
	return &Element{data: data, validUntil: validUntil, onExpire: onExpire}
}

type Cache struct {
	data kitSync.Map
}

func NewCache() *Cache {
	return &Cache{
		data: *kitSync.NewMap(),
	}
}

func (c *Cache) LoadOrStore(key interface{}, e *Element) (actual *Element, loaded bool) {
	now := time.Now()
	c.data.ReplaceWithFunc(key, func(oldValue interface{}, oldLoaded bool) (newValue interface{}, delete bool) {
		if oldLoaded {
			o := oldValue.(*Element)
			if !o.IsExpired(now) {
				actual = o
				return o, false
			}
		}
		actual = e
		return e, false
	})
	return actual, actual != e
}

func (c *Cache) Load(key interface{}) (actual *Element) {
	a, loaded := c.data.Load(key)
	if !loaded {
		return nil
	}
	actual = a.(*Element)
	if actual.IsExpired(time.Now()) {
		return nil
	}
	return actual
}

func (c *Cache) Delete(key interface{}) {
	c.data.Delete(key)
}

func (c *Cache) HandleExpiredElements(now time.Time) {
	m := make(map[interface{}]*Element)
	c.data.Range(func(key, value interface{}) bool {
		m[key] = value.(*Element)
		return true
	})
	for k, e := range m {
		if e.IsExpired(now) {
			c.data.Delete(k)
			e.onExpire(e.data)
		}
	}
}
