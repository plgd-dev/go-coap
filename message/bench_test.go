package message

import "testing"

func BenchmarkPathOption(b *testing.B) {
	buf := make([]byte, 256)
	b.ResetTimer()
	for i := uint32(0); i < uint32(b.N); i++ {
		options := make(Options, 0, 10)
		path := "a/b/c"

		options, bufLen, err := options.SetPath(buf, path)
		if err != nil {
			b.Fatalf("unexpected error %d", err)
		}
		if bufLen != 3 {
			b.Fatalf("unexpected length %d", bufLen)
		}

		v := make([]string, 3)
		n, err := options.GetStrings(URIPath, v)
		if n != 3 {
			b.Fatalf("bad length")
		}
		if err != nil {
			b.Fatalf("unexpected code")
		}
	}
}
