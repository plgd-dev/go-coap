package cache

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoadOrStore(t *testing.T) {
	c := NewCache[int, string]()

	c.LoadOrStore(1, c.NewElement("expired", time.Now().Add(-time.Hour), nil))
	c.LoadOrStore(2, c.NewElement("unexpired", time.Now().Add(time.Hour), func(d string) {
		require.FailNow(t, "invalid invocation of onExpire function")
	}))

	// e := c.Load(42)
}
