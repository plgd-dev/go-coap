package rand_test

import (
	"sync"
	"testing"

	"github.com/plgd-dev/go-coap/v3/pkg/rand"
)

func TestRand(*testing.T) {
	r := rand.NewRand(0)
	_ = r.Int63()
	_ = r.Uint32()
}

func TestMultiThreadedRand(*testing.T) {
	r := rand.NewRand(0)
	var done sync.WaitGroup
	for i := 0; i < 100; i++ {
		done.Add(1)
		go func(index int) {
			if index%2 == 0 {
				_ = r.Int63()
			} else {
				_ = r.Uint32()
			}
			done.Done()
		}(i)
	}
	done.Wait()
}
