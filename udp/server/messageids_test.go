package server

import (
	"net"
	"sync"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/stretchr/testify/require"
)

func TestEndpointMessageIDsEviction(t *testing.T) {
	ids := newEndpointMessageIDs(2)
	seeds := 0
	initial := func() uint32 {
		seeds++
		return uint32(seeds)
	}
	first := ids.get("first", initial)
	second := ids.get("second", initial)
	require.Same(t, first, ids.get("first", initial))
	third := ids.get("third", initial)
	require.NotSame(t, first, third)
	require.Same(t, first, ids.get("first", initial))
	require.NotSame(t, second, ids.get("second", initial))
	require.Equal(t, 4, seeds)
	require.Len(t, ids.entries, 2)
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
	first, created := s.getOrCreateConn(listener, victim, local)
	require.True(t, created)
	var last int32
	for range 20 {
		last = first.GetMessageID()
	}
	getClose(first)()

	other, created := s.getOrCreateConn(listener, attacker, local)
	require.True(t, created)
	for range 1 << 16 {
		other.GetMessageID()
		message.GetMID()
	}

	reconnected, created := s.getOrCreateConn(listener, victim, local)
	require.True(t, created)
	require.Equal(t, (last+1)&0xffff, reconnected.GetMessageID())
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
	first, created := s.getOrCreateConn(listener, remote, local1)
	require.True(t, created)
	second, created := s.getOrCreateConn(listener, remote, local2)
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
