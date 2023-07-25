package client

import "testing"

func BenchmarkM(b *testing.B) {
	m := NewMutexMap()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// run uncontended lock/unlock - should be quite fast
		m.Lock(i).Unlock()
	}
}
