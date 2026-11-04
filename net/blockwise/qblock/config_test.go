package qblock

import (
	"context"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/stretchr/testify/require"
)

func TestConfigDefaultsAndDerivedTimers(t *testing.T) {
	config, err := (Config{}).Resolve()
	require.NoError(t, err)
	require.Equal(t, DefaultConfig(), config)
	require.Equal(t, 247*time.Second, config.NonPartialTimeout())
	require.Equal(t, 247*time.Second, config.NonProbingWait(2*time.Second))
	require.Equal(t, 248*time.Second, config.NonProbingWait(3*time.Second))
	for range 100 {
		delay := config.nonDelay()
		require.GreaterOrEqual(t, delay, config.NonTimeout)
		require.Less(t, delay, config.NonTimeout+config.NonTimeout/2)
		require.GreaterOrEqual(t, config.NonReceiveTimeout-delay, time.Second)
	}
	config.NonTimeout, config.NonReceiveTimeout = 4*time.Second, 7*time.Second
	config.NonMaxRetransmit, config.MaxLatency = 2, 10*time.Second
	resolved, err := config.Resolve()
	require.NoError(t, err)
	require.Equal(t, 42*time.Second, resolved.NonPartialTimeout())
	require.Equal(t, 44*time.Second, resolved.NonProbingWait(6*time.Second))
}

func TestConfigLargestDerivedTimersDoNotOverflow(t *testing.T) {
	config := Config{
		MaxPayloads: 256, NonTimeout: time.Hour, NonReceiveTimeout: 2 * time.Hour,
		NonMaxRetransmit: 20, MaxLatency: 24 * time.Hour,
	}
	resolved, err := config.Resolve()
	require.NoError(t, err)
	want := time.Duration(((1<<20)-1)*90+48*60+60) * time.Minute
	require.Equal(t, want, resolved.NonPartialTimeout())
	require.Equal(t, want+30*time.Minute, resolved.NonProbingWait(90*time.Minute))
	require.Positive(t, resolved.NonReceiveTimeout<<resolved.NonMaxRetransmit)
}

func TestConfigRejectsInvalidAndOverflowingParameters(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Config)
	}{
		{"zero-burst", func(c *Config) { c.MaxPayloads = 0 }},
		{"large-burst", func(c *Config) { c.MaxPayloads = 257 }},
		{"negative-timeout", func(c *Config) { c.NonTimeout = -time.Second }},
		{"large-timeout", func(c *Config) { c.NonTimeout = time.Duration(1<<63 - 1) }},
		{"receive-guard", func(c *Config) { c.NonReceiveTimeout = 3999 * time.Millisecond }},
		{"negative-receive", func(c *Config) { c.NonReceiveTimeout = -time.Second }},
		{"backoff-overflow", func(c *Config) { c.NonReceiveTimeout = time.Duration(1<<63 - 1) }},
		{"negative-retries", func(c *Config) { c.NonMaxRetransmit = -1 }},
		{"large-retries", func(c *Config) { c.NonMaxRetransmit = 21 }},
		{"zero-latency", func(c *Config) { c.MaxLatency = 0 }},
		{"large-latency", func(c *Config) { c.MaxLatency = 25 * time.Hour }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			tc.edit(&config)
			_, err := config.Resolve()
			require.ErrorIs(t, err, ErrInvalidConfig)
			engine := New(newTestClient(), nil)
			require.ErrorIs(t, engine.SetConfig(config), ErrInvalidConfig)
			require.Equal(t, DefaultConfig(), engine.config, "invalid config must not mutate engine")
		})
	}
}

func TestConfiguredSetBoundariesAndMissingReports(t *testing.T) {
	for _, size := range []int64{1, 3, 16, 64, 256} {
		engine := New(newTestClient(), nil)
		config := DefaultConfig()
		config.MaxPayloads = size
		require.NoError(t, engine.SetConfig(config))
		request := qTestRequest(t, "a", struct {
			num  int64
			more bool
		}{size + min(size-1, 1), true})
		numbers, whole, err := engine.q2RequestedBlocks(request, SZX16, size*3)
		require.NoError(t, err)
		require.Equal(t, size*2-1, numbers[len(numbers)-1])
		require.Equal(t, size == 1, whole)
		assembly, err := NewAssembly(SZX16, 16*size*3, 16*size*3, int(size*3))
		require.NoError(t, err)
		require.Len(t, assembly.missingSet(size), int(size))
	}
}

type deadlineExchange struct {
	deadline time.Time
}

func (*deadlineExchange) NewToken() (message.Token, error) { return []byte("retry"), nil }
func (*deadlineExchange) Send(*pool.Message) error         { return nil }
func (*deadlineExchange) Close()                           {}
func (e *deadlineExchange) Receive(ctx context.Context) (*pool.Message, error) {
	e.deadline, _ = ctx.Deadline()
	return nil, context.DeadlineExceeded
}

func TestConfiguredRecoveryBackoffAndExhaustion(t *testing.T) {
	engine := New(newTestClient(), nil)
	config := DefaultConfig()
	config.NonReceiveTimeout, config.NonMaxRetransmit = 5*time.Second, 2
	require.NoError(t, engine.SetConfig(config))
	request := pool.NewMessage(context.Background())
	request.SetCode(codes.GET)
	request.SetToken([]byte("body"))
	exchange := new(deadlineExchange)
	receiver := q2BodyReceiver[*testClient]{
		b: engine, req: request, exchange: exchange, requestedSet: -1,
	}
	for retry := 0; retry <= config.NonMaxRetransmit; retry++ {
		start := time.Now()
		_, err := receiver.receive()
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.WithinDuration(t, start.Add(config.NonReceiveTimeout<<retry), exchange.deadline, 100*time.Millisecond)
		err = receiver.recover(context.DeadlineExceeded)
		if retry == config.NonMaxRetransmit {
			require.Error(t, err)
			require.ErrorIs(t, err, context.DeadlineExceeded)
		} else {
			require.NoError(t, err)
		}
	}
}
