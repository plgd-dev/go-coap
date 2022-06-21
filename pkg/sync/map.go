package sync

import (
	"sync"

	"golang.org/x/exp/maps"
)

// Map is like a Go map[interface{}]interface{} but is safe for concurrent use by multiple goroutines.
type Map[K comparable, V any] struct {
	mutex sync.RWMutex
	data  map[K]V
}

// NewMap creates map.
func NewMap[K comparable, V any]() *Map[K, V] {
	return &Map[K, V]{
		data: make(map[K]V),
	}
}

// Store sets the value for a key.
func (m *Map[K, V]) Store(key K, value V) {
	m.StoreWithFunc(key, func() V { return value })
}

func (m *Map[K, V]) StoreWithFunc(key K, createFunc func() V) {
	v := createFunc()
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.data[key] = v
}

// Load returns the value stored in the map for a key, or nil if no value is present. The loaded value is read-only and should not be modified.
// The ok result indicates whether value was found in the map.
func (m *Map[K, V]) Load(key K) (value V, ok bool) {
	return m.LoadWithFunc(key, nil)
}

func (m *Map[K, V]) LoadWithFunc(key K, onLoadFunc func(value V) V) (V, bool) {
	m.mutex.RLock()
	value, ok := m.data[key]
	defer m.mutex.RUnlock()
	if ok && onLoadFunc != nil {
		value = onLoadFunc(value)
	}
	return value, ok
}

// LoadOrStore returns the existing value for the key if present. The loaded value is read-only and should not be modified.
// Otherwise, it stores and returns the given value. The loaded result is true if the value was loaded, false if stored.
func (m *Map[K, V]) LoadOrStore(key K, value V) (actual V, loaded bool) {
	return m.LoadOrStoreWithFunc(key, nil, func() V { return value })
}

func (m *Map[K, V]) LoadOrStoreWithFunc(key K, onLoadFunc func(value V) V, createFunc func() V) (actual V, loaded bool) {
	m.mutex.RLock()
	v, ok := m.data[key]
	m.mutex.RUnlock()
	if ok {
		if onLoadFunc != nil {
			v = onLoadFunc(v)
		}
		return v, ok
	}
	v = createFunc()
	m.mutex.Lock()
	m.data[key] = v
	m.mutex.Unlock()
	return v, ok
}

// Range calls f sequentially for each key and value present in the map. If f returns false, range stops the iteration.
// Note: Range does not copy the whole map, instead the read lock is locked on iteration of the map, and unlocked before f is called.
func (m *Map[K, V]) Range(f func(key K, value V) bool) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	for key, value := range m.data {
		m.mutex.RUnlock()
		ok := f(key, value)
		m.mutex.RLock()
		if !ok {
			return
		}
	}
}

// Range2 calls f sequentially for each key and value present in the map. If f returns false, range stops the iteration.
// Note: The function copies the whole map under a read lock and then iterates this copy unlocked.
func (m *Map[K, V]) Range2(f func(key K, value V) bool) {
	mCopy := make(map[K]V)
	m.mutex.RLock()
	maps.Copy(mCopy, m.data)
	m.mutex.RUnlock()
	for key, value := range mCopy {
		ok := f(key, value)
		if !ok {
			return
		}
	}
}

// Replace replaces the existing value with a new value and returns old value for the key.
func (m *Map[K, V]) Replace(key K, value V) (oldValue V, oldLoaded bool) {
	return m.ReplaceWithFunc(key, func(oldValue V, oldLoaded bool) (newValue V, doDelete bool) {
		return value, false
	})
}

func (m *Map[K, V]) ReplaceWithFunc(key K, onReplaceFunc func(oldValue V, oldLoaded bool) (newValue V, doDelete bool)) (oldValue V, oldLoaded bool) {
	m.mutex.RLock()
	v, ok := m.data[key]
	m.mutex.RUnlock()
	newValue, del := onReplaceFunc(v, ok)
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if del {
		delete(m.data, key)
		return v, ok
	}
	m.data[key] = newValue
	return v, ok
}

// Delete deletes the value for a key.
func (m *Map[K, V]) Delete(key K) (deleted bool) {
	return m.DeleteWithFunc(key, nil)
}

func (m *Map[K, V]) DeleteWithFunc(key K, onDeleteFunc func(value V)) (deleted bool) {
	m.mutex.Lock()
	value, ok := m.data[key]
	delete(m.data, key)
	m.mutex.Unlock()
	if ok && onDeleteFunc != nil {
		onDeleteFunc(value)
	}
	return ok
}

// PullOut loads and deletes the value for a key.
func (m *Map[K, V]) PullOut(key K) (value V, ok bool) {
	return m.PullOutWithFunc(key, nil)
}

func (m *Map[K, V]) PullOutWithFunc(key K, onLoadFunc func(value V) V) (V, bool) {
	m.mutex.Lock()
	value, ok := m.data[key]
	delete(m.data, key)
	m.mutex.Unlock()
	if ok && onLoadFunc != nil {
		value = onLoadFunc(value)
	}
	return value, ok
}

// PullOutAll extracts internal map data and replace it with empty map.
func (m *Map[K, V]) PullOutAll() map[K]V {
	m.mutex.Lock()
	data := m.data
	m.data = make(map[K]V)
	m.mutex.Unlock()
	return data
}

// Length returns number of stored values.
func (m *Map[K, V]) Length() int {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	return len(m.data)
}
