package cache

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

func TestLoadOrStoreWithLimit(t *testing.T) {
	c := NewCache[int, string]()
	first := NewElement("first", time.Time{}, nil)
	actual, loaded, err := c.LoadOrStoreWithLimit(1, first, 1)
	require.NoError(t, err)
	require.False(t, loaded)
	require.Same(t, first, actual)

	actual, loaded, err = c.LoadOrStoreWithLimit(1, NewElement("unused", time.Time{}, nil), 1)
	require.NoError(t, err)
	require.True(t, loaded)
	require.Same(t, first, actual)

	actual, loaded, err = c.LoadOrStoreWithLimit(2, NewElement("second", time.Time{}, nil), 1)
	require.ErrorIs(t, err, ErrCacheFull)
	require.False(t, loaded)
	require.Nil(t, actual)
	require.Equal(t, 1, c.Length())

	c.Delete(1)
	_, loaded, err = c.LoadOrStoreWithLimit(2, NewElement("second", time.Time{}, nil), 1)
	require.NoError(t, err)
	require.False(t, loaded)
	require.Equal(t, 1, c.Length())
}

func TestLoadOrStoreWithLimitExpiration(t *testing.T) {
	for _, limit := range []int{0, 1} {
		for _, sameKey := range []bool{false, true} {
			t.Run(fmt.Sprintf("limit=%d/sameKey=%t", limit, sameKey), func(t *testing.T) {
				c := NewCache[int, string]()
				expiredCalls := 0
				c.Store(1, NewElement("expired", time.Now().Add(-time.Second), func(value string) {
					require.Equal(t, "expired", value)
					// Expiration callbacks must run outside the map lock.
					_ = c.Length()
					expiredCalls++
				}))
				key := 2
				if sameKey {
					key = 1
				}
				fresh := NewElement("fresh", time.Time{}, nil)
				actual, loaded, err := c.LoadOrStoreWithLimit(key, fresh, limit)
				require.NoError(t, err)
				require.False(t, loaded)
				require.Same(t, fresh, actual)
				if sameKey || limit > 0 {
					require.Equal(t, 1, expiredCalls)
					require.Equal(t, 1, c.Length())
				} else {
					require.Zero(t, expiredCalls)
				}
			})
		}
	}
}

func TestLoadOrStoreWithLimitConcurrent(t *testing.T) {
	for _, limit := range []int{-1, 0, 1, 8} {
		for _, sameKey := range []bool{false, true} {
			t.Run(fmt.Sprintf("limit=%d/sameKey=%t", limit, sameKey), func(t *testing.T) {
				testLoadOrStoreWithLimitConcurrent(t, limit, sameKey)
			})
		}
	}
}

func testLoadOrStoreWithLimitConcurrent(t *testing.T, limit int, sameKey bool) {
	t.Helper()
	const requests = 128
	c := NewCache[int, int]()
	start := make(chan struct{})
	results := make(chan error, requests)
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := i
			if sameKey {
				key = 0
			}
			<-start
			_, _, err := c.LoadOrStoreWithLimit(key, NewElement(i, time.Time{}, nil), limit)
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else {
			require.ErrorIs(t, err, ErrCacheFull)
		}
	}
	expected := limit
	if limit <= 0 || sameKey {
		expected = requests
	}
	require.Equal(t, expected, accepted)
	if sameKey {
		expected = 1
	}
	require.Equal(t, expected, c.Length())
}

func TestLoadOrStoreWithLimitConcurrentExpiration(t *testing.T) {
	c := NewCache[int, string]()
	var expiredCalls atomic.Int32
	c.Store(1, NewElement("expired", time.Now().Add(-time.Second), func(string) {
		expiredCalls.Inc()
	}))
	const requests = 128
	start := make(chan struct{})
	results := make(chan *Element[string], requests)
	errs := make(chan error, requests)
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			actual, _, err := c.LoadOrStoreWithLimit(1, NewElement("fresh", time.Time{}, nil), 1)
			results <- actual
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	actual := c.Load(1)
	require.NotNil(t, actual)
	for result := range results {
		require.Same(t, actual, result)
	}
	require.Equal(t, int32(1), expiredCalls.Load())
	require.Equal(t, 1, c.Length())
}

func TestAddElement(t *testing.T) {
	cache := NewCache[string, string]()

	elementToCache := string("elem")
	elem := NewElement(elementToCache, time.Now().Add(1*time.Minute), nil)
	loadedElem, loaded := cache.LoadOrStore("abcd", elem)

	require.False(t, loaded)
	require.Equal(t, elementToCache, loadedElem.data)

	elementToCache2 := string("elem2")
	elem2 := NewElement(elementToCache2, time.Now().Add(1*time.Minute), nil)

	loadedElem2, loaded2 := cache.LoadOrStore("abcdefg", elem2)
	require.False(t, loaded2)
	require.Equal(t, elementToCache2, loadedElem2.data)

	elementToCache3 := string("elem")
	elem3 := NewElement(elementToCache3, time.Now().Add(1*time.Minute), nil)

	loadedElem, loaded = cache.LoadOrStore("abcd", elem3)
	require.True(t, loaded)
	require.Equal(t, elementToCache3, loadedElem.data)
}

func TestLoadElement(t *testing.T) {
	cache := NewCache[string, string]()

	loadedElem := cache.Load("abcd")
	require.Nil(t, loadedElem)

	elementToCache := string("elem")
	elem := NewElement(elementToCache, time.Now().Add(1*time.Minute), nil)
	loadedElem, loaded := cache.LoadOrStore("abcd", elem)

	require.False(t, loaded)
	require.Equal(t, elementToCache, loadedElem.data)
}

func TestDeleteElement(t *testing.T) {
	cache := NewCache[string, string]()

	loadedElem := cache.Load("abcd")
	require.Nil(t, loadedElem)

	elementToCache := string("elem")
	elem := NewElement(elementToCache, time.Now().Add(1*time.Minute), nil)
	loadedElem, loaded := cache.LoadOrStore("abcd", elem)

	require.False(t, loaded)
	require.Equal(t, elementToCache, loadedElem.data)

	loadedElem = cache.Load("abcd")
	require.Equal(t, elementToCache, loadedElem.data)

	cache.Delete("abcd")
	loadedElem = cache.Load("abcd")
	require.Nil(t, loadedElem)
}

func TestElementExpiration(t *testing.T) {
	expirationInvoked := false
	cache := NewCache[string, string]()

	elementToCache := string("elem")
	elem := NewElement(elementToCache, time.Now().Add(1*time.Second), func(string) {
		expirationInvoked = true
	})
	loadedElem, _ := cache.LoadOrStore("abcd", elem)

	elementToCache = string("elem")
	elem = NewElement(elementToCache, time.Time{}, nil)
	cache.LoadOrStore("abcdef", elem)

	require.False(t, expirationInvoked)
	require.False(t, loadedElem.IsExpired(time.Now()))
	require.True(t, loadedElem.IsExpired(time.Now().Add(2*time.Second)))
	require.False(t, expirationInvoked)
	cache.CheckExpirations(time.Now().Add(2 * time.Second))
	require.True(t, expirationInvoked)

	require.False(t, elem.IsExpired(time.Now()))
	require.False(t, elem.IsExpired(time.Now().Add(time.Hour)))
}

func TestRangeFunction(t *testing.T) {
	cache := NewCache[string, string]()

	loadedElem := cache.Load("abcd")
	require.Nil(t, loadedElem)

	elementToCache := string("elem")
	elem := NewElement(elementToCache, time.Now().Add(1*time.Minute), nil)
	cache.LoadOrStore("abcd", elem)

	elementToCache = string("elem2")
	elem = NewElement(elementToCache, time.Now().Add(1*time.Minute), nil)
	cache.LoadOrStore("abcdef", elem)

	actualMap := make(map[string]string)
	expectedMap := make(map[string]string)
	expectedMap["abcd"] = "elem"
	expectedMap["abcdef"] = "elem2"
	foundElements := 0

	cache.Range(func(key string, value *Element[string]) bool {
		actualMap[key] = value.Data()
		foundElements++
		return true
	})

	require.Equal(t, 2, foundElements)

	for k := range actualMap {
		_, contains := actualMap[k]
		require.True(t, contains)
		require.Equal(t, expectedMap[k], actualMap[k])
	}
}

func TestPullOutAllFunction(t *testing.T) {
	cache := NewCache[string, string]()

	elementToCache := string("elem")
	elem := NewElement(elementToCache, time.Now().Add(1*time.Minute), nil)
	cache.LoadOrStore("abcd", elem)

	elementToCache = string("elem2")
	elem = NewElement(elementToCache, time.Now().Add(1*time.Minute), nil)
	cache.LoadOrStore("abcdef", elem)

	cache.LoadAndDeleteAll()

	foundElements := 0
	cache.Range(func(string, *Element[string]) bool {
		foundElements++
		return true
	})
	require.Equal(t, 0, foundElements)
}
