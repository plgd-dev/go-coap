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
	ids := newEndpointMessageIDs(2)
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
	ids.release("first", now)
	_, err = ids.acquire("third", initial, now.Add(client.ExchangeLifetime-time.Nanosecond))
	require.ErrorIs(t, err, ErrEndpointMessageIDsFull)
	firstAgain, err := ids.acquire("first", initial, now)
	require.NoError(t, err)
	require.Same(t, first, firstAgain)
	ids.release("first", now)
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
	ids := newEndpointMessageIDs(1)
	now := time.Now()
	initial := func() uint32 { return 100 }
	first, err := ids.acquire("first", initial, now)
	require.NoError(t, err)
	second, err := ids.acquire("first", initial, now)
	require.NoError(t, err)
	require.Same(t, first, second)
	ids.release("first", now)
	_, err = ids.acquire("other", initial, now.Add(2*client.ExchangeLifetime))
	require.ErrorIs(t, err, ErrEndpointMessageIDsFull)
	closed := now.Add(2 * client.ExchangeLifetime)
	ids.release("first", closed)
	_, err = ids.acquire("other", initial, closed.Add(client.ExchangeLifetime-time.Nanosecond))
	require.ErrorIs(t, err, ErrEndpointMessageIDsFull)
	_, err = ids.acquire("other", initial, closed.Add(client.ExchangeLifetime))
	require.NoError(t, err)
}

type endpointMessageIDLimitOption uint32

func (o endpointMessageIDLimitOption) UDPServerApply(cfg *Config) {
	cfg.MaxEndpointMessageIDs = uint32(o)
}

func TestServerEndpointMessageIDLimit(t *testing.T) {
	for _, limit := range []uint32{0, 1, 2} {
		t.Run(strconv.FormatUint(uint64(limit), 10), func(t *testing.T) {
			s := New(endpointMessageIDLimitOption(limit))
			defer s.Stop()
			initial := func() uint32 { return 100 }
			now := time.Now()
			first, err := s.messageIDs.acquire("first", initial, now)
			require.NoError(t, err)
			first.Add(1)
			s.messageIDs.release("first", now)
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
	ids := newEndpointMessageIDs(0)
	initial := func() uint32 { return 100 }
	now := time.Now()
	first, err := ids.acquire("first", initial, now)
	require.NoError(t, err)
	ids.release("first", now)
	for i := 0; i < int(DefaultMaxEndpointMessageIDs)+1; i++ {
		key := strconv.Itoa(i)
		_, acquireErr := ids.acquire(key, initial, now)
		require.NoError(t, acquireErr)
		ids.release(key, now)
	}
	require.Len(t, ids.entries, int(DefaultMaxEndpointMessageIDs)+2)
	again, err := ids.acquire("first", initial, now.Add(2*client.ExchangeLifetime))
	require.NoError(t, err)
	require.Same(t, first, again)
}

func TestActiveEndpointMessageIDsSurviveCapacityPressure(t *testing.T) {
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
	s := New(endpointMessageIDLimitOption(1))
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
	s := New(endpointMessageIDLimitOption(1))
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
				s.messageIDs.release(remote.String(), time.Now())
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
