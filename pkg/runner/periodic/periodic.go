package periodic

import (
	"sync"
	"time"

	"go.uber.org/atomic"
)

type Func = func(f func(now time.Time) bool)

func New(stop <-chan struct{}, tick time.Duration) Func {
	var idx atomic.Uint64
	var m sync.Map
	go func() {
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			var now time.Time
			select {
			case now = <-t.C:
			case <-stop:
				return
			}
			v := make(map[uint64]func(time.Time) bool)
			m.Range(func(key, value interface{}) bool {
				v[key.(uint64)] = value.(func(time.Time) bool) //nolint:forcetypeassert
				return true
			})
			for k, f := range v {
				if ok := f(now); !ok {
					m.Delete(k)
				}
			}
		}
	}()
	return func(f func(time.Time) bool) {
		if f == nil {
			return
		}
		v := idx.Add(1)
		m.Store(v, f)
	}
}
