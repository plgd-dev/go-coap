package coap

import (
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	coapNet "github.com/go-ocf/go-coap/net"
	dtls "github.com/pion/dtls/v2"
	"github.com/stretchr/testify/require"
)

func TestKeepAliveTCP_Client(t *testing.T) {
	s, err := coapNet.NewTCPListener("tcp", ":0", time.Millisecond*100)
	require.NoError(t, err)
	defer s.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	c := &Client{
		Net: "tcp",
		KeepAlive: KeepAlive{
			Enable:      true,
			WaitForPong: time.Microsecond,
			Interval:    time.Millisecond * 100,
			NewRetryPolicy: func() RetryFunc {
				now := time.Now()
				return func() (time.Time, error) {
					c := time.Now()
					if c.Before(now.Add(time.Second * 2)) {
						return c.Add(time.Millisecond * 200), nil
					}
					return time.Time{}, ErrKeepAliveDeadlineExceeded
				}
			},
		},
		NotifySessionEndFunc: func(err error) {
			fmt.Printf("NotifySessionEndFunc %v\n", err)
			if err == ErrKeepAliveDeadlineExceeded {
				defer wg.Done()
			}
		},
	}

	co, err := c.Dial(s.Addr().String())
	require.NoError(t, err)
	defer co.Close()

	wg.Wait()
}

func TestKeepAliveTCPTLS_Client(t *testing.T) {
	cert, err := tls.X509KeyPair(CertPEMBlock, KeyPEMBlock)
	require.NoError(t, err)
	config := tls.Config{
		Certificates: []tls.Certificate{cert},
	}
	s, err := coapNet.NewTLSListener("tcp", ":0", &config, time.Millisecond*100)
	require.NoError(t, err)
	defer s.Close()
	go func() {
		a, err := s.Accept()
		if err != nil {
			return
		}
		defer a.Close()
		buf := make([]byte, 1024)
		for {
			_, err := a.Read(buf)
			if err != nil {
				return
			}
		}
	}()

	var wg sync.WaitGroup
	wg.Add(1)
	c := &Client{
		Net:       "tcp-tls",
		TLSConfig: &tls.Config{InsecureSkipVerify: true},
		KeepAlive: KeepAlive{
			Enable:      true,
			WaitForPong: time.Microsecond,
			Interval:    time.Millisecond * 100,
			NewRetryPolicy: func() RetryFunc {
				now := time.Now()
				return func() (time.Time, error) {
					c := time.Now()
					if c.Before(now.Add(time.Second * 2)) {
						return c.Add(time.Millisecond * 200), nil
					}
					return time.Time{}, ErrKeepAliveDeadlineExceeded
				}
			},
		},
		NotifySessionEndFunc: func(err error) {
			if err == ErrKeepAliveDeadlineExceeded {
				defer wg.Done()
			}
		},
	}

	co, err := c.Dial(s.Addr().String())
	require.NoError(t, err)
	defer co.Close()

	wg.Wait()
}

func TestKeepAliveTCP_Server(t *testing.T) {
	l, err := coapNet.NewTCPListener("tcp", ":0", time.Millisecond*100)
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Add(1)

	s := &Server{Listener: l, ReadTimeout: time.Second * 3600, WriteTimeout: time.Second * 3600,
		NotifySessionNewFunc: func(s *ClientConn) {
			fmt.Printf("networkSession start %v\n", s.RemoteAddr())
		}, NotifySessionEndFunc: func(w *ClientConn, err error) {
			if err == ErrKeepAliveDeadlineExceeded {
				defer wg.Done()
			}
			fmt.Printf("networkSession end %v: %v\n", w.RemoteAddr(), err)
		},
		KeepAlive: KeepAlive{
			Enable:      true,
			WaitForPong: time.Microsecond,
			Interval:    time.Millisecond * 100,
			NewRetryPolicy: func() RetryFunc {
				now := time.Now()
				return func() (time.Time, error) {
					c := time.Now()
					if c.Before(now.Add(time.Second * 2)) {
						return c.Add(time.Millisecond * 200), nil
					}
					return time.Time{}, ErrKeepAliveDeadlineExceeded
				}
			},
		},
	}
	defer s.Shutdown()

	go func() {
		s.ActivateAndServe()
	}()

	co, err := net.Dial("tcp", l.Addr().String())
	require.NoError(t, err)
	defer co.Close()

	wg.Wait()
}

func TestKeepAliveTCPTLS_Server(t *testing.T) {
	cert, err := tls.X509KeyPair(CertPEMBlock, KeyPEMBlock)
	require.NoError(t, err)
	config := tls.Config{
		Certificates: []tls.Certificate{cert},
	}
	l, err := coapNet.NewTLSListener("tcp", ":0", &config, time.Millisecond*100)
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Add(1)

	s := &Server{Listener: l, ReadTimeout: time.Second * 3600, WriteTimeout: time.Second * 3600,
		NotifySessionNewFunc: func(s *ClientConn) {
			fmt.Printf("networkSession start %v\n", s.RemoteAddr())
		}, NotifySessionEndFunc: func(w *ClientConn, err error) {
			if err == ErrKeepAliveDeadlineExceeded {
				defer wg.Done()
			}
			fmt.Printf("networkSession end %v: %v\n", w.RemoteAddr(), err)
		},
		KeepAlive: KeepAlive{
			Enable:      true,
			WaitForPong: time.Microsecond,
			Interval:    time.Millisecond * 100,
			NewRetryPolicy: func() RetryFunc {
				now := time.Now()
				return func() (time.Time, error) {
					c := time.Now()
					if c.Before(now.Add(time.Second * 2)) {
						return c.Add(time.Millisecond * 200), nil
					}
					return time.Time{}, ErrKeepAliveDeadlineExceeded
				}
			},
		},
	}
	defer s.Shutdown()

	go func() {
		s.ActivateAndServe()
	}()

	co, err := tls.Dial("tcp", l.Addr().String(), &tls.Config{InsecureSkipVerify: true})
	require.NoError(t, err)
	defer co.Close()

	wg.Wait()
}

func TestKeepAliveUDP_Server(t *testing.T) {
	a, err := net.ResolveUDPAddr("udp", ":0")
	require.NoError(t, err)
	l, err := net.ListenUDP("udp", a)
	require.NoError(t, err)
	connUDP := coapNet.NewConnUDP(l, time.Millisecond*100, 2, func(err error) { t.Log(err) })

	var wg sync.WaitGroup
	wg.Add(1)

	s := &Server{ReadTimeout: time.Second * 3600, WriteTimeout: time.Second * 3600,
		NotifySessionNewFunc: func(s *ClientConn) {
			fmt.Printf("networkSession start %v\n", s.RemoteAddr())
		}, NotifySessionEndFunc: func(w *ClientConn, err error) {
			if err == ErrKeepAliveDeadlineExceeded {
				defer wg.Done()
			}
			fmt.Printf("networkSession end %v: %v\n", w.RemoteAddr(), err)
		},
		KeepAlive: KeepAlive{
			Enable:      true,
			WaitForPong: time.Microsecond,
			Interval:    time.Millisecond * 100,
			NewRetryPolicy: func() RetryFunc {
				now := time.Now()
				return func() (time.Time, error) {
					c := time.Now()
					if c.Before(now.Add(time.Second * 2)) {
						return c.Add(time.Millisecond * 200), nil
					}
					return time.Time{}, ErrKeepAliveDeadlineExceeded
				}
			},
		},
	}
	defer s.Shutdown()

	go func() {
		s.activateAndServe(nil, nil, connUDP)
	}()

	co, err := net.Dial("udp", l.LocalAddr().String())
	require.NoError(t, err)
	defer co.Close()

	_, err = co.Write([]byte{0x40, 0x1, 0x30, 0x39})
	require.NoError(t, err)

	wg.Wait()
}

func TestKeepAliveDTLS_Server(t *testing.T) {
	config := &dtls.Config{
		PSK: func(hint []byte) ([]byte, error) {
			fmt.Printf("Hint: %s \n", hint)
			return []byte{0xAB, 0xC1, 0x23}, nil
		},
		PSKIdentityHint: []byte("peer dtls"),
		CipherSuites:    []dtls.CipherSuiteID{dtls.TLS_PSK_WITH_AES_128_CCM_8},
	}

	l, err := coapNet.NewDTLSListener("udp", ":0", config, time.Millisecond*100)
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Add(1)

	s := &Server{Listener: l, ReadTimeout: time.Second * 3600, WriteTimeout: time.Second * 3600,
		NotifySessionNewFunc: func(s *ClientConn) {
			fmt.Printf("networkSession start %v\n", s.RemoteAddr())
		}, NotifySessionEndFunc: func(w *ClientConn, err error) {
			if err == ErrKeepAliveDeadlineExceeded {
				defer wg.Done()
			}
			fmt.Printf("networkSession end %v: %v\n", w.RemoteAddr(), err)
		},
		KeepAlive: KeepAlive{
			Enable:      true,
			WaitForPong: time.Microsecond,
			Interval:    time.Millisecond * 100,
			NewRetryPolicy: func() RetryFunc {
				now := time.Now()
				return func() (time.Time, error) {
					c := time.Now()
					if c.Before(now.Add(time.Second * 2)) {
						return c.Add(time.Millisecond * 200), nil
					}
					return time.Time{}, ErrKeepAliveDeadlineExceeded
				}
			},
		},
	}
	defer s.Shutdown()

	go func() {
		s.ActivateAndServe()
	}()

	a, err := net.ResolveUDPAddr("udp", l.Addr().String())
	require.NoError(t, err)
	co, err := dtls.Dial("udp", a, config)
	require.NoError(t, err)
	defer co.Close()

	wg.Wait()
}

func TestKeepAliveUDP_Client(t *testing.T) {
	a, err := net.ResolveUDPAddr("udp", ":0")
	require.NoError(t, err)
	pc, err := net.ListenUDP("udp", a)
	require.NoError(t, err)
	l := coapNet.NewConnUDP(pc, time.Millisecond*100, 2, func(err error) { t.Log(err) })

	defer l.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	c := &Client{
		Net: "udp",
		KeepAlive: KeepAlive{
			Enable:      true,
			WaitForPong: time.Microsecond,
			Interval:    time.Millisecond * 100,
			NewRetryPolicy: func() RetryFunc {
				now := time.Now()
				return func() (time.Time, error) {
					c := time.Now()
					if c.Before(now.Add(time.Second * 2)) {
						return c.Add(time.Millisecond * 200), nil
					}
					return time.Time{}, ErrKeepAliveDeadlineExceeded
				}
			},
		},
		NotifySessionEndFunc: func(err error) {
			if err == ErrKeepAliveDeadlineExceeded {
				defer wg.Done()
			}
		},
	}

	co, err := c.Dial(l.LocalAddr().String())
	require.NoError(t, err)
	defer co.Close()

	wg.Wait()
}

func TestKeepAliveDTLS_Client(t *testing.T) {
	config := &dtls.Config{
		PSK: func(hint []byte) ([]byte, error) {
			fmt.Printf("Hint: %s \n", hint)
			return []byte{0xAB, 0xC1, 0x23}, nil
		},
		PSKIdentityHint: []byte("peer dtls"),
		CipherSuites:    []dtls.CipherSuiteID{dtls.TLS_PSK_WITH_AES_128_CCM_8},
	}

	l, err := coapNet.NewDTLSListener("udp", ":0", config, time.Millisecond*100)
	require.NoError(t, err)
	defer l.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	c := &Client{
		Net:        "udp-dtls",
		DTLSConfig: config,
		KeepAlive: KeepAlive{
			Enable:      true,
			WaitForPong: time.Microsecond,
			Interval:    time.Millisecond * 100,
			NewRetryPolicy: func() RetryFunc {
				now := time.Now()
				return func() (time.Time, error) {
					c := time.Now()
					if c.Before(now.Add(time.Second * 2)) {
						return c.Add(time.Millisecond * 200), nil
					}
					return time.Time{}, ErrKeepAliveDeadlineExceeded
				}
			},
		},
		NotifySessionEndFunc: func(err error) {
			if err == ErrKeepAliveDeadlineExceeded {
				defer wg.Done()
			}
		},
	}

	co, err := c.Dial(l.Addr().String())
	require.NoError(t, err)
	defer co.Close()

	wg.Wait()
}

func TestKeepAliveUDP_Client_NoResponse(t *testing.T) {
	a, err := net.ResolveUDPAddr("udp", ":0")
	require.NoError(t, err)
	pc, err := net.ListenUDP("udp", a)
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Add(1)
	c := &Client{
		Net:       "udp",
		KeepAlive: MustMakeKeepAlive(time.Second * 1),
		NotifySessionEndFunc: func(err error) {
			if err == ErrKeepAliveDeadlineExceeded {
				defer wg.Done()
			}
		},
	}

	co, err := c.Dial(pc.LocalAddr().String())
	require.NoError(t, err)
	defer co.Close()
	err = co.WriteMsg(co.NewMessage(MessageParams{}))
	require.NoError(t, err)

	wg.Wait()
}
