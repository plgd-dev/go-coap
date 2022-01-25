package pool

import (
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

func TestMessageSetPath(t *testing.T) {
	msg := NewMessage()
	size := 4
	for i := 0; i < 8; i++ {
		buffer := make([]byte, size)
		buffer[0] = '/'
		_, err := rand.Read(buffer[1:])
		require.NoError(t, err)
		inPath := string(buffer)
		msg.SetPath(inPath)
		outPath, err := msg.Path()
		require.NoError(t, err)
		require.Equal(t, inPath, outPath)
		size = size * 4
	}
}
