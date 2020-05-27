package message

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEncodeUint32(t *testing.T) {
	type args struct {
		value uint32
	}
	tests := []struct {
		name    string
		args    args
		want    int
		wantErr bool
	}{
		{
			name: "0",
			args: args{0},
		},
		{
			name: "256",
			args: args{256},
			want: 2,
		},
		{
			name: "16384",
			args: args{16384},
			want: 2,
		},
		{
			name: "5000000",
			args: args{5000000},
			want: 3,
		},
		{
			name: "20000000",
			args: args{20000000},
			want: 4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := make([]byte, 4)
			got, err := EncodeUint32(buf, tt.args.value)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			buf = buf[:got]
			val, n, err := DecodeUint32(buf)
			require.NoError(t, err)
			require.Equal(t, len(buf), n)
			require.Equal(t, tt.args.value, val)
		})
	}
}
