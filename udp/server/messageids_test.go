package server

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options/config"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

func TestEndpointMessageIDsEviction(t *testing.T) {
	ids := newEndpointMessageIDs(2, false)
	now := time.Now()
	seeds := 0
	initial := func() uint32 {
		seeds++
		return uint32(seeds)
	}
	first, err := ids.acquire("first", initial, now)
	require.NoError(t, err)
	second, err := ids.acquire("second", initial, now)
	require.NoError(t, err)
	require.NoError(t, ids.release("first", now))
	_, err = ids.acquire("third", initial, now.Add(client.ExchangeLifetime-time.Nanosecond))
	require.ErrorIs(t, err, ErrEndpointMessageIDsFull)
	firstAgain, err := ids.acquire("first", initial, now)
	require.NoError(t, err)
	require.Same(t, first, firstAgain)
	require.NoError(t, ids.release("first", now))
	third, err := ids.acquire("third", initial, now.Add(client.ExchangeLifetime))
	require.NoError(t, err)
	require.NotSame(t, first, third)
	secondAgain, err := ids.acquire("second", initial, now.Add(client.ExchangeLifetime))
	require.NoError(t, err)
	require.Same(t, second, secondAgain)
	require.Equal(t, 3, seeds)
	require.Len(t, ids.entries, 2)
}

func TestEndpointMessageIDsRetentionAfterLastConnection(t *testing.T) {
	ids := newEndpointMessageIDs(1, false)
	now := time.Now()
	initial := func() uint32 { return 100 }
	first, err := ids.acquire("first", initial, now)
	require.NoError(t, err)
	second, err := ids.acquire("first", initial, now)
	require.NoError(t, err)
	require.Same(t, first, second)
	require.NoError(t, ids.release("first", now))
	_, err = ids.acquire("other", initial, now.Add(2*client.ExchangeLifetime))
	require.ErrorIs(t, err, ErrEndpointMessageIDsFull)
	closed := now.Add(2 * client.ExchangeLifetime)
	require.NoError(t, ids.release("first", closed))
	_, err = ids.acquire("other", initial, closed.Add(client.ExchangeLifetime-time.Nanosecond))
	require.ErrorIs(t, err, ErrEndpointMessageIDsFull)
	_, err = ids.acquire("other", initial, closed.Add(client.ExchangeLifetime))
	require.NoError(t, err)
}

func TestEndpointMessageIDsReleaseRequiresActiveEntry(t *testing.T) {
	ids := newEndpointMessageIDs(1, false)
	now := time.Now()
	require.Error(t, ids.release("missing", now))
	_, err := ids.acquire("first", func() uint32 { return 1 }, now)
	require.NoError(t, err)
	require.NoError(t, ids.release("first", now))
	require.Error(t, ids.release("first", now))
	require.Zero(t, ids.entries["first"].users)
	require.Len(t, ids.byOrder, 1)
}

type endpointMessageIDCacheOption EndpointMessageIDCacheConfig

func (o endpointMessageIDCacheOption) UDPServerApply(cfg *Config) {
	cfg.EndpointMessageIDCache = EndpointMessageIDCacheConfig(o)
}

func TestServerEndpointMessageIDLimit(t *testing.T) {
	for _, limit := range []uint32{1, 2} {
		t.Run(strconv.FormatUint(uint64(limit), 10), func(t *testing.T) {
			s := New(endpointMessageIDCacheOption{MaxEntries: limit})
			defer s.Stop()
			initial := func() uint32 { return 100 }
			now := time.Now()
			first, err := s.messageIDs.acquire("first", initial, now)
			require.NoError(t, err)
			first.Add(1)
			require.NoError(t, s.messageIDs.release("first", now))
			second, err := s.messageIDs.acquire("second", initial, now)
			if limit == 1 {
				require.ErrorIs(t, err, ErrEndpointMessageIDsFull)
				require.Nil(t, second)
				require.Len(t, s.messageIDs.entries, 1)
			} else {
				require.NoError(t, err)
				require.NotSame(t, first, second)
				require.Len(t, s.messageIDs.entries, 2)
			}
			got, err := s.messageIDs.acquire("first", initial, now)
			require.NoError(t, err)
			require.Same(t, first, got)
			require.Equal(t, uint32(101), got.Load())
		})
	}
}

func TestEndpointMessageIDsUnlimited(t *testing.T) {
	s := New(endpointMessageIDCacheOption{Unlimited: true})
	defer s.Stop()
	require.NoError(t, s.validateConfig())
	ids := s.messageIDs
	initial := func() uint32 { return 100 }
	now := time.Now()
	first, err := ids.acquire("first", initial, now)
	require.NoError(t, err)
	require.NoError(t, ids.release("first", now))
	for i := 0; i < int(DefaultMaxEndpointMessageIDs)+1; i++ {
		key := strconv.Itoa(i)
		_, acquireErr := ids.acquire(key, initial, now)
		require.NoError(t, acquireErr)
		require.NoError(t, ids.release(key, now))
	}
	require.Len(t, ids.entries, int(DefaultMaxEndpointMessageIDs)+2)
	again, err := ids.acquire("first", initial, now.Add(2*client.ExchangeLifetime))
	require.NoError(t, err)
	require.Same(t, first, again)
}

func TestActiveEndpointMessageIDsSurviveCapacityPressure(t *testing.T) {
	config := DefaultEndpointMessageIDCacheConfig()
	config.OverflowMode = EndpointMessageIDFailClosed
	s := New(endpointMessageIDCacheOption(config))
	defer s.Stop()
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	remote := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
	local1 := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5683}
	local2 := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 2), Port: 5683}
	first, created, err := s.getOrCreateConn(listener, remote, local1)
	require.NoError(t, err)
	require.True(t, created)
	last := first.GetMessageID()
	for i := 0; i < int(DefaultMaxEndpointMessageIDs)+1; i++ {
		other := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 2), Port: 10000 + i}
		cc, otherCreated, getErr := s.getConn(listener, other, local1, true)
		if i >= int(DefaultMaxEndpointMessageIDs)-1 {
			require.ErrorIs(t, getErr, ErrEndpointMessageIDsFull)
			require.Nil(t, cc)
			require.False(t, otherCreated)
			continue
		}
		require.NoError(t, getErr)
		require.True(t, otherCreated)
		getClose(cc)()
	}
	second, created, err := s.getOrCreateConn(listener, remote, local2)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, (last+1)&0xffff, second.GetMessageID())
	getClose(first)()
	_, _, err = s.getConn(listener, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 3), Port: 12345}, local1, true)
	require.ErrorIs(t, err, ErrEndpointMessageIDsFull)
	require.Equal(t, (last+2)&0xffff, second.GetMessageID())
	getClose(second)()
}

func TestServerEndpointCapacityReleasedAfterLifetime(t *testing.T) {
	s := New(endpointMessageIDCacheOption{MaxEntries: 1, OverflowMode: EndpointMessageIDFailClosed})
	defer s.Stop()
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	local := listener.LocalAddr().(*net.UDPAddr)
	first := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
	second := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12346}
	cc, created, err := s.getConn(listener, first, local, true)
	require.NoError(t, err)
	require.True(t, created)
	last := cc.GetMessageID()
	getClose(cc)()
	_, _, err = s.getConn(listener, second, local, true)
	require.ErrorIs(t, err, ErrEndpointMessageIDsFull)
	reconnected, created, err := s.getConn(listener, first, local, true)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, (last+1)&0xffff, reconnected.GetMessageID())
	getClose(reconnected)()
	s.connsMutex.Lock()
	s.messageIDs.entries[first.String()].expires = time.Now().Add(-time.Second)
	s.connsMutex.Unlock()
	cc, created, err = s.getConn(listener, second, local, true)
	require.NoError(t, err)
	require.True(t, created)
	getClose(cc)()
}

func TestServerReportsEndpointCapacity(t *testing.T) {
	s := New(endpointMessageIDCacheOption{MaxEntries: 1, OverflowMode: EndpointMessageIDFailClosed})
	reported := make(chan error, 1)
	s.cfg.Errors = func(err error) { reported <- err }
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	served := make(chan error, 1)
	go func() { served <- s.Serve(listener) }()
	defer func() {
		s.Stop()
		require.NoError(t, <-served)
	}()
	first, err := net.DialUDP("udp4", nil, listener.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	defer first.Close()
	second, err := net.DialUDP("udp4", nil, listener.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	defer second.Close()
	ping := []byte{0x40, 0x00, 0x00, 0x01}
	_, err = first.Write(ping)
	require.NoError(t, err)
	require.NoError(t, first.SetReadDeadline(time.Now().Add(time.Second)))
	_, err = first.Read(make([]byte, 16))
	require.NoError(t, err)
	_, err = second.Write(ping)
	require.NoError(t, err)
	select {
	case reportedErr := <-reported:
		require.ErrorIs(t, reportedErr, ErrEndpointMessageIDsFull)
	case <-time.After(time.Second):
		t.Fatal("server did not report endpoint capacity exhaustion")
	}
	_, err = s.NewConn(second.LocalAddr().(*net.UDPAddr))
	require.ErrorIs(t, err, ErrEndpointMessageIDsFull)
}

func TestServerFailOpenPreservesAvailability(t *testing.T) {
	s := New(endpointMessageIDCacheOption{MaxEntries: 1})
	defer s.Stop()
	var warnings []error
	s.cfg.Errors = func(err error) { warnings = append(warnings, err) }
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	local := listener.LocalAddr().(*net.UDPAddr)
	first := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
	second := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12346}
	cached, created, err := s.getConn(listener, first, local, true)
	require.NoError(t, err)
	require.True(t, created)
	last := cached.GetMessageID()
	overflow, created, err := s.getConn(listener, second, local, true)
	require.NoError(t, err)
	require.True(t, created)
	require.Len(t, warnings, 1)
	require.ErrorIs(t, warnings[0], ErrEndpointMessageIDsFull)
	require.Len(t, s.messageIDs.entries, 1)
	otherLocal := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 2), Port: local.Port}
	overflow2, created, err := s.getConn(listener, second, otherLocal, true)
	require.NoError(t, err)
	require.True(t, created)
	require.Len(t, warnings, 1)
	require.Equal(t, (last+1)&0xffff, cached.GetMessageID())
	getClose(cached)()
	s.connsMutex.Lock()
	s.messageIDs.entries[first.String()].expires = time.Now().Add(-time.Second)
	s.connsMutex.Unlock()
	tracked, created, err := s.getConn(listener, second, local, true)
	require.NoError(t, err)
	require.False(t, created)
	require.Same(t, overflow, tracked)
	thirdLocal := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 3), Port: local.Port}
	tracked, created, err = s.getConn(listener, second, thirdLocal, true)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, 1, s.messageIDs.entries[second.String()].users)
	getClose(overflow)()
	getClose(overflow2)()
	require.Equal(t, 1, s.messageIDs.entries[second.String()].users)
	getClose(tracked)()
	require.Zero(t, s.messageIDs.entries[second.String()].users)
	require.Len(t, warnings, 1)
}

func TestServerZeroEndpointMessageIDCache(t *testing.T) {
	s := New(endpointMessageIDCacheOption{})
	defer s.Stop()
	require.NoError(t, s.validateConfig())
	var warnings []error
	s.cfg.Errors = func(err error) { warnings = append(warnings, err) }
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	remote := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
	local1 := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5683}
	local2 := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 2), Port: 5683}
	first, created, err := s.getOrCreateConn(listener, remote, local1)
	require.NoError(t, err)
	require.True(t, created)
	second, created, err := s.getOrCreateConn(listener, remote, local2)
	require.NoError(t, err)
	require.True(t, created)
	require.Empty(t, s.messageIDs.entries)
	require.Empty(t, warnings)
	getClose(first)()
	getClose(second)()
	reconnected, created, err := s.getOrCreateConn(listener, remote, local1)
	require.NoError(t, err)
	require.True(t, created)
	require.Empty(t, s.messageIDs.entries)
	getClose(reconnected)()
	require.Empty(t, warnings)
}

func TestEndpointMessageIDOverflowWarningInterval(t *testing.T) {
	s := New()
	defer s.Stop()
	now := time.Now()
	require.True(t, s.shouldReportMIDOverflow(now))
	require.False(t, s.shouldReportMIDOverflow(now.Add(time.Minute-time.Nanosecond)))
	require.True(t, s.shouldReportMIDOverflow(now.Add(time.Minute)))
}

func TestServerFailOpenHandlesDatagramsAtCapacity(t *testing.T) {
	s := New(endpointMessageIDCacheOption{MaxEntries: 1})
	reported := make(chan error, 2)
	s.cfg.Errors = func(err error) { reported <- err }
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	served := make(chan error, 1)
	go func() { served <- s.Serve(listener) }()
	defer func() {
		s.Stop()
		require.NoError(t, <-served)
	}()
	first, err := net.DialUDP("udp4", nil, listener.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	defer first.Close()
	second, err := net.DialUDP("udp4", nil, listener.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	defer second.Close()
	ping := []byte{0x40, 0x00, 0x00, 0x01}
	for _, peer := range []*net.UDPConn{first, second} {
		_, err = peer.Write(ping)
		require.NoError(t, err)
		require.NoError(t, peer.SetReadDeadline(time.Now().Add(time.Second)))
		buf := make([]byte, 16)
		n, readErr := peer.Read(buf)
		require.NoError(t, readErr)
		require.Equal(t, []byte{0x70, 0x00, 0x00, 0x01}, buf[:n])
	}
	select {
	case reportedErr := <-reported:
		require.ErrorIs(t, reportedErr, ErrEndpointMessageIDsFull)
	case <-time.After(time.Second):
		t.Fatal("server did not report cache overflow")
	}
}

func TestInvalidEndpointMessageIDOverflowMode(t *testing.T) {
	s := New(endpointMessageIDCacheOption{MaxEntries: 1, OverflowMode: 99})
	defer s.Stop()
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	require.ErrorContains(t, s.Serve(listener), "invalid endpoint message ID overflow mode")
	_, _, err = s.getConn(listener, listener.LocalAddr().(*net.UDPAddr), listener.LocalAddr().(*net.UDPAddr), true)
	require.ErrorContains(t, err, "invalid endpoint message ID overflow mode")
}

func TestInvalidEndpointMessageIDCacheConfig(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config endpointMessageIDCacheOption
		err    string
	}{
		{"unlimited with limit", endpointMessageIDCacheOption{MaxEntries: 1, Unlimited: true}, "requires MaxEntries to be zero"},
		{"zero fail-closed", endpointMessageIDCacheOption{OverflowMode: EndpointMessageIDFailClosed}, "requires fail-open"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(tc.config)
			defer s.Stop()
			listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
			require.NoError(t, err)
			defer listener.Close()
			require.ErrorContains(t, s.Serve(listener), tc.err)
			addr := listener.LocalAddr().(*net.UDPAddr)
			_, _, err = s.getConn(listener, addr, addr, true)
			require.ErrorContains(t, err, tc.err)
		})
	}
}

func TestServerMessageIDsSurviveReconnectAndOtherPeers(t *testing.T) {
	s := New()
	defer s.Stop()
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	local := listener.LocalAddr().(*net.UDPAddr)
	victim := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
	attacker := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12346}
	first, created, err := s.getOrCreateConn(listener, victim, local)
	require.NoError(t, err)
	require.True(t, created)
	var last int32
	previous := make(map[int32]bool)
	for range 20 {
		last = first.GetMessageID()
		previous[last] = true
	}
	getClose(first)()

	other, created, err := s.getOrCreateConn(listener, attacker, local)
	require.NoError(t, err)
	require.True(t, created)
	for range 1 << 16 {
		other.GetMessageID()
		message.GetMID()
	}

	reconnected, created, err := s.getOrCreateConn(listener, victim, local)
	require.NoError(t, err)
	require.True(t, created)
	for i := int32(1); i <= 20; i++ {
		id := reconnected.GetMessageID()
		require.Equal(t, (last+i)&0xffff, id)
		require.False(t, previous[id], "reconnected peer must not reuse a previous message ID")
	}
	getClose(reconnected)()
	getClose(other)()
}

func TestServerMessageIDsSharedAcrossLocalAddresses(t *testing.T) {
	s := New()
	defer s.Stop()
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	remote := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
	local1 := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5683}
	local2 := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 2), Port: 5683}
	first, created, err := s.getOrCreateConn(listener, remote, local1)
	require.NoError(t, err)
	require.True(t, created)
	second, created, err := s.getOrCreateConn(listener, remote, local2)
	require.NoError(t, err)
	require.True(t, created)
	last := first.GetMessageID()
	require.Equal(t, (last+1)&0xffff, second.GetMessageID())
	ids := make(chan int32, 2000)
	var wg sync.WaitGroup
	for _, conn := range []*client.Conn{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 1000 {
				ids <- conn.GetMessageID()
			}
		}()
	}
	wg.Wait()
	close(ids)
	seen := make(map[int32]bool)
	for id := range ids {
		require.False(t, seen[id])
		seen[id] = true
	}
	getClose(first)()
	getClose(second)()
}

func TestPeerMIDCollisionAcrossReconnect(t *testing.T) {
	s := New()
	defer s.Stop()
	var calls int
	completed := make(chan struct{}, 2)
	s.cfg.Handler = func(w *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
		calls++
		if err := w.SetResponse(codes.Content, message.TextPlain, nil); err != nil {
			t.Errorf("set response: %v", err)
		}
	}
	s.cfg.ProcessReceivedMessage = func(req *pool.Message, cc *client.Conn, handler config.HandlerFunc[*client.Conn]) {
		cc.ProcessReceivedMessageWithHandler(req, handler)
		completed <- struct{}{}
	}
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer peer.Close()
	remote := peer.LocalAddr().(*net.UDPAddr)
	local := listener.LocalAddr().(*net.UDPAddr)
	var previous int32 = -1
	for range 2 {
		cc, created, connErr := s.getOrCreateConn(listener, remote, local)
		require.NoError(t, connErr)
		require.True(t, created)
		pong := make(chan struct{}, 1)
		cancel, pingErr := cc.AsyncPing(func() { pong <- struct{}{} })
		require.NoError(t, pingErr)
		require.NoError(t, peer.SetReadDeadline(time.Now().Add(time.Second)))
		buf := make([]byte, 1024)
		n, _, readErr := peer.ReadFromUDP(buf)
		require.NoError(t, readErr)
		var ping message.Message
		_, err = coder.DefaultCoder.Decode(buf[:n], &ping)
		require.NoError(t, err)
		if previous >= 0 {
			require.Equal(t, (previous+1)&0xffff, ping.MessageID)
		}
		previous = ping.MessageID
		req := cc.AcquireMessage(context.Background())
		req.SetType(message.Confirmable)
		req.SetCode(codes.GET)
		req.SetToken([]byte{1})
		req.SetMessageID(ping.MessageID)
		datagram, marshalErr := req.MarshalWithEncoder(coder.DefaultCoder)
		require.NoError(t, marshalErr)
		require.NoError(t, cc.Process(nil, datagram))
		select {
		case <-completed:
		case <-time.After(time.Second):
			t.Fatal("request was not processed")
		}
		n, _, readErr = peer.ReadFromUDP(buf)
		require.NoError(t, readErr)
		var response message.Message
		_, err = coder.DefaultCoder.Decode(buf[:n], &response)
		require.NoError(t, err)
		require.Equal(t, message.Acknowledgement, response.Type)
		require.Equal(t, ping.MessageID, response.MessageID)
		require.Equal(t, codes.Content, response.Code)
		req.SetType(message.Acknowledgement)
		req.SetCode(codes.Empty)
		req.SetToken(nil)
		datagram, marshalErr = req.MarshalWithEncoder(coder.DefaultCoder)
		require.NoError(t, marshalErr)
		require.NoError(t, cc.Process(nil, datagram))
		select {
		case <-pong:
		case <-time.After(time.Second):
			t.Fatal("outgoing ping was not acknowledged")
		}
		cc.ReleaseMessage(req)
		cancel()
		getClose(cc)()
	}
	require.Equal(t, 2, calls)
}

func TestIncomingMessageIDsDoNotAdvanceSharedSequence(t *testing.T) {
	const errorMode = "error"
	for _, mode := range []string{"accepted", "dropped", errorMode} {
		for _, seed := range []uint32{1000, 65534} {
			t.Run(mode+"/"+strconv.FormatUint(uint64(seed), 10), func(t *testing.T) {
				s := New()
				defer s.Stop()
				completed := make(chan struct{}, 2)
				monitorErr := errors.New("monitor rejected request")
				s.cfg.RequestMonitor = func(_ *client.Conn, _ *pool.Message) (bool, error) {
					switch mode {
					case "dropped":
						return true, nil
					case errorMode:
						return false, monitorErr
					default:
						return false, nil
					}
				}
				s.cfg.Handler = func(w *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
					if err := w.SetResponse(codes.Content, message.TextPlain, nil); err != nil {
						t.Errorf("set response: %v", err)
					}
				}
				s.cfg.ProcessReceivedMessage = func(req *pool.Message, cc *client.Conn, handler config.HandlerFunc[*client.Conn]) {
					cc.ProcessReceivedMessageWithHandler(req, handler)
					completed <- struct{}{}
				}
				listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
				require.NoError(t, err)
				defer listener.Close()
				remote := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
				local1 := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5683}
				local2 := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 2), Port: 5683}
				_, err = s.messageIDs.acquire(remote.String(), func() uint32 { return seed }, time.Now())
				require.NoError(t, err)
				require.NoError(t, s.messageIDs.release(remote.String(), time.Now()))
				first, created, err := s.getOrCreateConn(listener, remote, local1)
				require.NoError(t, err)
				require.True(t, created)
				second, created, err := s.getOrCreateConn(listener, remote, local2)
				require.NoError(t, err)
				require.True(t, created)
				last := first.GetMessageID()
				req := second.AcquireMessage(context.Background())
				defer second.ReleaseMessage(req)
				req.SetType(message.Confirmable)
				req.SetCode(codes.GET)
				req.SetToken([]byte{1})
				for _, mid := range []int32{last, (last + 32767) & 0xffff} {
					req.SetMessageID(mid)
					datagram, marshalErr := req.MarshalWithEncoder(coder.DefaultCoder)
					require.NoError(t, marshalErr)
					err = second.Process(nil, datagram)
					if mode == errorMode {
						require.ErrorIs(t, err, monitorErr)
					} else {
						require.NoError(t, err)
					}
					if mode == "accepted" {
						select {
						case <-completed:
						case <-time.After(time.Second):
							t.Fatal("request was not processed")
						}
					}
				}
				require.Equal(t, (last+1)&0xffff, first.GetMessageID())
				require.Equal(t, (last+2)&0xffff, second.GetMessageID())
				getClose(first)()
				reconnected, created, err := s.getOrCreateConn(listener, remote, local1)
				require.NoError(t, err)
				require.True(t, created)
				require.Equal(t, (last+3)&0xffff, reconnected.GetMessageID())
				getClose(reconnected)()
				getClose(second)()
			})
		}
	}
}
