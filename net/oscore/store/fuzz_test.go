package store

import (
	"os"
	"path/filepath"
	"testing"
)

func FuzzStoreFileReaders(f *testing.F) {
	f.Add([]byte(`{"version":1,"next":256}`))
	f.Add([]byte(`{"version":1,"next":512}`))
	f.Add([]byte{0x09, 0x14}) // RFC 8613 Appendix C.4 OSCORE option.
	f.Add([]byte{0x83, 0x68, 0x45, 0x6e, 0x63, 0x72, 0x79, 0x70, 0x74, 0x30})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > maxStateBytes*2 {
			return
		}
		path := filepath.Join(t.TempDir(), "state")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, _ = readSequenceWithExistence(path)
		_, _, _ = readSequencePending(path)
		_, _, _ = readReplayHighWater(path)
	})
}
