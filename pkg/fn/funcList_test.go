package fn_test

import (
	"testing"

	"github.com/plgd-dev/go-coap/v3/pkg/fn"
	"github.com/stretchr/testify/require"
)

func TestFuncList(t *testing.T) {
	fns := make(fn.FuncList, 0, 2)

	counter := 0
	// functions should execute in reverse order they were added in
	second := 0
	fns = append(fns, func() {
		second = counter
		counter++
	})
	first := 0
	fns = append(fns, func() {
		first = counter
		counter++
	})

	fns.Execute()
	require.Equal(t, 0, first)
	require.Equal(t, 1, second)
}
