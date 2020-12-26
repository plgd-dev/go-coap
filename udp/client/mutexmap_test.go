package client

import (
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMutexMap(t *testing.T) {

	r := rand.New(rand.NewSource(42))

	m := NewMutexMap()
	_ = m

	keyCount := 20
	iCount := 10000
	out := make(chan string, iCount*2)

	// run a bunch of concurrent requests for various keys,
	// the idea is to have a lot of lock contention
	var wg sync.WaitGroup
	wg.Add(iCount)
	for i := 0; i < iCount; i++ {
		go func(rn int) {
			defer wg.Done()
			key := strconv.Itoa(rn)

			// you can prove the test works by commenting the locking out and seeing it fail
			l := m.Lock(key)
			defer l.Unlock()

			out <- key + " A"
			time.Sleep(time.Microsecond) // make 'em wait a mo'
			out <- key + " B"
		}(r.Intn(keyCount))
	}
	wg.Wait()
	close(out)

	// verify the map is empty now
	if l := len(m.ma); l != 0 {
		t.Errorf("unexpected map length at test end: %v", l)
	}

	// confirm that the output always produced the correct sequence
	outLists := make([][]string, keyCount)
	for s := range out {
		sParts := strings.Fields(s)
		kn, err := strconv.Atoi(sParts[0])
		if err != nil {
			t.Fatal(err)
		}
		outLists[kn] = append(outLists[kn], sParts[1])
	}
	for kn := 0; kn < keyCount; kn++ {
		l := outLists[kn] // list of output for this particular key
		for i := 0; i < len(l); i += 2 {
			if l[i] != "A" || l[i+1] != "B" {
				t.Errorf("For key=%v and i=%v got unexpected values %v and %v", kn, i, l[i], l[i+1])
				break
			}
		}
	}
	if t.Failed() {
		t.Logf("Failed, outLists: %#v", outLists)
	}

}

func BenchmarkM(b *testing.B) {
	m := NewMutexMap()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// run uncontended lock/unlock - should be quite fast
		m.Lock(i).Unlock()
	}
}
