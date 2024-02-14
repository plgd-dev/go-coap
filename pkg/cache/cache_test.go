package cache

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

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

	require.Equal(t, foundElements, 2)

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
	require.Equal(t, foundElements, 0)
}
