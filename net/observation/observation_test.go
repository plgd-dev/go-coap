package observation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestValidSequenceNumber(t *testing.T) {
	type args struct {
		old             uint32
		new             uint32
		lastEventOccurs time.Time
		now             time.Time
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "(1 << 25)-1, 0, now-1s, now",
			args: args{
				old:             (1 << 25)-1,
				new:             0,
				lastEventOccurs: time.Now().Add(-time.Second),
				now:             time.Now(),
			},
			want: true,
		},
		{
			name: "0, 1, 0, now",
			args: args{
				new: 1,
				now: time.Now(),
			},
			want: true,
		},
		{
			name: "1582, 1583, now-1s, now",
			args: args{
				old:             1582,
				new:             1583,
				lastEventOccurs: time.Now().Add(-time.Second),
				now:             time.Now(),
			},
			want: true,
		},
		{
			name: "1582, 1, now-129s, now",
			args: args{
				old:             1582,
				new:             1,
				lastEventOccurs: time.Now().Add(-time.Second * 129),
				now:             time.Now(),
			},
			want: true,
		},
		{
			name: "1582, 1, now-125s, now",
			args: args{
				old:             1582,
				new:             1,
				lastEventOccurs: time.Now().Add(-time.Second * 125),
				now:             time.Now(),
			},
			want: false,
		},
		{
			name: "1 << 23, 0, now-1s, now",
			args: args{
				old:             1 << 23,
				new:             0,
				lastEventOccurs: time.Now().Add(-time.Second),
				now:             time.Now(),
			},
			want: false,
		},
		{
			name: "0, 1 << 23+1, now-1s, now",
			args: args{
				old:             0,
				new:             1 << 23+1,
				lastEventOccurs: time.Now().Add(-time.Second),
				now:             time.Now(),
			},
			want: false,
		},
		{
			name: "1582, 1582, now-1s, now",
			args: args{
				old:             1582,
				new:             1582,
				lastEventOccurs: time.Now().Add(-time.Second),
				now:             time.Now(),
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ValidSequenceNumber(tt.args.old, tt.args.new, tt.args.lastEventOccurs, tt.args.now)
			assert.Equal(t, tt.want, got)
		})
	}
}
