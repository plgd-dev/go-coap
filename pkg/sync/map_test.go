package sync

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLength(t *testing.T) {
	m := NewMap[int, string]()
	m.Store(1, "1")
	require.Equal(t, 1, m.Length())
	m.Store(1, "2")
	require.Equal(t, 1, m.Length())
	m.Store(2, "2")
	require.Equal(t, 2, m.Length())
}

func getTestMapContent() map[int]string {
	return map[int]string{
		1: "one",
		2: "two",
		3: "three",
		4: "four",
		5: "five",
	}
}

func TestCopyData(t *testing.T) {
	src := getTestMapContent()

	m := NewMap[int, string]()
	for k, v := range src {
		m.StoreWithFunc(k, func() string {
			return v
		})
	}
	for k, v := range src {
		value, ok := m.Load(k)
		require.True(t, ok)
		require.Equal(t, v, value)
	}

	c := m.CopyData()
	require.Equal(t, src, c)
}

func TestLoad(t *testing.T) {
	src := getTestMapContent()

	m := NewMap[int, string]()
	for k, v := range src {
		m.Store(k, v)
	}
	for k, v := range src {
		value, ok := m.Load(k)
		require.True(t, ok)
		require.Equal(t, v, value)
	}

	value, ok := m.LoadWithFunc(1, func(v string) string {
		return "prefix" + v
	})
	require.True(t, ok)
	require.Equal(t, "prefix"+src[1], value)
}

func TestLoadOrStore(t *testing.T) {
	src := getTestMapContent()

	m := NewMap[int, string]()
	for k, v := range src {
		value, loaded := m.LoadOrStore(k, v)
		require.False(t, loaded)
		require.Equal(t, v, value)
	}

	newV := "forty two"
	v1, l1 := m.LoadOrStoreWithFunc(42, func(string) string {
		require.FailNow(t, "unexpected load call")
		return ""
	}, func() string {
		return newV
	})
	require.False(t, l1)
	require.Equal(t, newV, v1)

	for k, v := range src {
		vr, lr := m.LoadOrStoreWithFunc(k, func(value string) string {
			return "prefix-" + value
		}, func() string {
			require.FailNow(t, "unexpected create call")
			return v
		})
		require.True(t, lr)
		require.Equal(t, "prefix-"+v, vr)
	}

	value, loaded := m.LoadOrStore(1, "first")
	require.True(t, loaded)
	require.NotEqual(t, "first", value)
}

func TestLoadOrStoreConcurrent(t *testing.T) {
	const goroutines = 8
	for attempt := 0; attempt < 1000; attempt++ {
		m := NewMap[int, int]()
		var start, done sync.WaitGroup
		start.Add(1)
		done.Add(goroutines)

		type result struct {
			value  int
			loaded bool
		}
		results := make([]result, goroutines)
		for i := 0; i < goroutines; i++ {
			go func(idx int) {
				defer done.Done()
				start.Wait()
				results[idx].value, results[idx].loaded = m.LoadOrStore(1, idx)
			}(i)
		}
		start.Done()
		done.Wait()

		var storedValue int
		stored := 0
		for _, result := range results {
			if !result.loaded {
				stored++
				storedValue = result.value
			}
		}
		for _, result := range results {
			require.Equal(t, storedValue, result.value)
		}
		require.Equal(t, 1, stored, "exactly one caller must store the value")
	}
}

func TestLoadOrStoreFromReadLockedCallback(t *testing.T) {
	m := NewMap[int, string]()
	m.Store(1, "one")

	m.Range2(func(key int, value string) bool {
		actual, loaded := m.LoadOrStore(key, "replacement")
		require.True(t, loaded)
		require.Equal(t, value, actual)
		return true
	})

	value, loaded := m.LoadWithFunc(1, func(value string) string {
		actual, loaded := m.LoadOrStore(1, "replacement")
		require.True(t, loaded)
		require.Equal(t, value, actual)
		return value
	})
	require.True(t, loaded)
	require.Equal(t, "one", value)
}

func testRange(t *testing.T, m *Map[int, string], rangeFn func(f func(key int, value string) bool)) {
	src := getTestMapContent()
	for k, v := range src {
		m.Store(k, v)
	}

	count := 0
	rangeFn(func(int, string) bool {
		count++
		return false
	})
	require.Equal(t, 1, count)

	rangeFn(func(key int, value string) bool {
		require.Equal(t, src[key], value)
		return true
	})
}

func TestRange(t *testing.T) {
	m := NewMap[int, string]()
	testRange(t, m, m.Range)
}

func TestRange2(t *testing.T) {
	m := NewMap[int, string]()
	testRange(t, m, m.Range2)
}

func TestReplace(t *testing.T) {
	src := getTestMapContent()
	m := NewMap[int, string]()
	for k, v := range src {
		m.Store(k, v)
	}

	newV := "first"
	oldV, ok := m.Replace(1, newV)
	require.True(t, ok)
	require.Equal(t, src[1], oldV)

	oldV, ok = m.ReplaceWithFunc(1, func(string, bool) (string, bool) {
		return "", true
	})
	require.True(t, ok)
	require.Equal(t, newV, oldV)
	require.Equal(t, len(src)-1, m.Length())

	newV = "forty two"
	_, ok = m.ReplaceWithFunc(42, func(string, bool) (string, bool) {
		return newV, false
	})
	require.False(t, ok)
	require.Equal(t, len(src), m.Length())
	v, ok := m.Load(42)
	require.True(t, ok)
	require.Equal(t, newV, v)
}

func TestDelete(t *testing.T) {
	src := getTestMapContent()
	m := NewMap[int, string]()
	for k, v := range src {
		m.Store(k, v)
	}

	m.Delete(42)
	require.Equal(t, len(src), m.Length())

	m.Delete(1)
	require.Equal(t, len(src)-1, m.Length())
	_, ok := m.Load(1)
	require.False(t, ok)

	m.DeleteWithFunc(1, func(string) {
		require.FailNow(t, "unexpected deleted value")
	})

	m.DeleteWithFunc(3, func(value string) {
		require.Equal(t, src[3], value)
	})
	require.Equal(t, len(src)-2, m.Length())
}

func TestLoadAndDelete(t *testing.T) {
	src := getTestMapContent()
	m := NewMap[int, string]()
	for k, v := range src {
		m.Store(k, v)
	}

	_, ok := m.LoadAndDelete(42)
	require.False(t, ok)

	v, ok := m.LoadAndDelete(2)
	require.True(t, ok)
	require.Equal(t, src[2], v)
	delete(src, 2)

	_, ok = m.LoadAndDeleteWithFunc(2, func(value string) string {
		require.FailNow(t, "unexpected pulled value")
		return value
	})
	require.False(t, ok)

	v, ok = m.LoadAndDeleteWithFunc(1, func(value string) string {
		return value + "-suffix"
	})
	require.True(t, ok)
	require.Equal(t, src[1]+"-suffix", v)
	delete(src, 1)

	data := m.LoadAndDeleteAll()
	require.Equal(t, 0, m.Length())
	require.Equal(t, src, data)
}
