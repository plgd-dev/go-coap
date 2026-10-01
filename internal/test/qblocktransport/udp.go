package qblocktransport

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"

	"github.com/plgd-dev/go-coap/v3/internal/test/qblocklink"
)

// UDPRelay owns two real IPv4 loopback sockets and serializes raw CoAP wire
// decisions through Controller. It accepts one client for one test workflow.
type UDPRelay struct {
	front, back *net.UDPConn
	controller  *Controller
	mu          sync.Mutex
	client      *net.UDPAddr
	errs        chan error
	done        chan struct{}
	once        sync.Once
}

func NewUDPRelay(target string, controller *Controller) (*UDPRelay, error) {
	addr, err := net.ResolveUDPAddr("udp4", target)
	if err != nil {
		return nil, err
	}
	front, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return nil, err
	}
	back, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		_ = front.Close()
		return nil, err
	}
	r := &UDPRelay{front: front, back: back, controller: controller, errs: make(chan error, 2), done: make(chan struct{})}
	var wg sync.WaitGroup
	wg.Add(2)
	for _, direction := range []qblocklink.Direction{qblocklink.ClientToServer, qblocklink.ServerToClient} {
		go func(d qblocklink.Direction) { defer wg.Done(); r.errs <- r.run(d) }(direction)
	}
	go func() { wg.Wait(); close(r.done) }()
	return r, nil
}
func (r *UDPRelay) Addr() net.Addr { return r.front.LocalAddr() }
func (r *UDPRelay) Close() error {
	var err error
	r.once.Do(func() { err = errors.Join(r.front.Close(), r.back.Close()) })
	return err
}
func (r *UDPRelay) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.done:
		return errors.Join(<-r.errs, <-r.errs)
	}
}
func (r *UDPRelay) run(d qblocklink.Direction) error {
	b := make([]byte, 65535)
	for {
		var n int
		var err error
		if d == qblocklink.ClientToServer {
			var peer *net.UDPAddr
			n, peer, err = r.front.ReadFromUDP(b)
			if err == nil {
				r.mu.Lock()
				if r.client == nil {
					r.client = peer
				}
				same := r.client.String() == peer.String()
				r.mu.Unlock()
				if !same {
					return errors.New("relay received another client")
				}
			}
		} else {
			n, err = r.back.Read(b)
		}
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		if err != nil {
			return err
		}
		packets, err := r.controller.process(d, b[:n])
		if err != nil {
			return err
		}
		for _, packet := range packets {
			if d == qblocklink.ClientToServer {
				n, err = r.back.Write(packet.Wire)
			} else {
				r.mu.Lock()
				peer := r.client
				r.mu.Unlock()
				if peer == nil {
					return errors.New("relay reply without client")
				}
				n, err = r.front.WriteToUDP(packet.Wire, peer)
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			if err != nil {
				return err
			}
			if n != len(packet.Wire) {
				return io.ErrShortWrite
			}
			r.controller.delivered(packet)
		}
	}
}
