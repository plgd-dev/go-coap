package coap

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-ocf/go-coap/codes"
	coapNet "github.com/go-ocf/go-coap/net"
	dtls "github.com/pion/dtls/v2"
)

func CreateRespMessageByReq(isTCP bool, code codes.Code, req Message) Message {
	if isTCP {
		resp := &TcpMessage{
			MessageBase{
				code:    code,
				payload: req.Payload(),
				token:   req.Token(),
			},
		}
		resp.SetPath(req.Path())
		resp.SetOption(ContentFormat, req.Option(ContentFormat))
		return resp
	}
	resp := &DgramMessage{
		MessageBase: MessageBase{
			typ:  Acknowledgement,
			code: code,

			payload: req.Payload(),
			token:   req.Token(),
		},
		messageID: req.MessageID(),
	}
	resp.SetPath(req.Path())
	resp.SetOption(ContentFormat, req.Option(ContentFormat))
	return resp
}

func EchoServer(w ResponseWriter, r *Request) {
	if r.Msg.IsConfirmable() {
		err := w.WriteMsg(CreateRespMessageByReq(r.Client.networkSession().IsTCP(), codes.Valid, r.Msg))
		if err != nil {
			fmt.Printf("Cannot write echo %v", err)
		}
	}
}

func EchoServerBadID(w ResponseWriter, r *Request) {
	if r.Msg.IsConfirmable() {
		w.WriteMsg(CreateRespMessageByReq(r.Client.networkSession().IsTCP(), codes.BadRequest, r.Msg))
	}
}

func RunLocalServerUDPWithHandler(lnet, laddr string, BlockWiseTransfer bool, BlockWiseTransferSzx BlockWiseSzx, handler HandlerFunc) (*Server, string, chan error, error) {
	return RunLocalServerUDPWithHandlerIfaces(lnet, laddr, BlockWiseTransfer, BlockWiseTransferSzx, handler, nil)
}

func RunLocalServerUDPWithHandlerIfaces(lnet, laddr string, BlockWiseTransfer bool, BlockWiseTransferSzx BlockWiseSzx, handler HandlerFunc, ifaces []net.Interface) (*Server, string, chan error, error) {
	network := strings.TrimSuffix(lnet, "-mcast")

	a, err := net.ResolveUDPAddr(network, laddr)
	if err != nil {
		return nil, "", nil, err
	}
	pc, err := net.ListenUDP(network, a)
	if err != nil {
		return nil, "", nil, err
	}

	connUDP := coapNet.NewConnUDP(pc, time.Millisecond*100, 2, func(err error) { fmt.Println(err) })
	if strings.Contains(lnet, "-mcast") {
		if ifaces == nil {
			ifaces, err = net.Interfaces()
			if err != nil {
				return nil, "", nil, err
			}
		}
		for _, iface := range ifaces {
			if err := connUDP.JoinGroup(&iface, a); err != nil {
				fmt.Printf("JoinGroup(%v, %v) %v", iface.Name, a, err)
			}
		}

		if err := connUDP.SetMulticastLoopback(true); err != nil {
			return nil, "", nil, fmt.Errorf("SetMulticastLoopback %w", err)
		}
	}

	server := &Server{ReadTimeout: time.Hour, WriteTimeout: time.Hour,
		NotifySessionNewFunc: func(s *ClientConn) {
			fmt.Printf("networkSession start %v\n", s.RemoteAddr())
		},
		NotifySessionEndFunc: func(w *ClientConn, err error) {
			fmt.Printf("networkSession end %v: %v\n", w.RemoteAddr(), err)
		},
		Handler:              handler,
		BlockWiseTransfer:    &BlockWiseTransfer,
		BlockWiseTransferSzx: &BlockWiseTransferSzx,
	}

	waitLock := sync.Mutex{}
	waitLock.Lock()
	server.NotifyStartedFunc = waitLock.Unlock

	// fin must be buffered so the goroutine below won't block
	// forever if fin is never read from. This always happens
	// in RunLocalUDPServer and can happen in TestShutdownUDP.
	fin := make(chan error, 1)

	go func() {
		err = server.activateAndServe(nil, nil, connUDP)
		connUDP.Close()
		fin <- err
	}()

	return server, pc.LocalAddr().String(), fin, nil
}

func RunLocalUDPServer(net, laddr string, BlockWiseTransfer bool, BlockWiseTransferSzx BlockWiseSzx) (*Server, string, chan error, error) {
	return RunLocalServerUDPWithHandler(net, laddr, BlockWiseTransfer, BlockWiseTransferSzx, nil)
}

func RunLocalServerTCPWithHandler(laddr string, BlockWiseTransfer bool, BlockWiseTransferSzx BlockWiseSzx, handler HandlerFunc) (*Server, string, chan error, error) {
	network := "tcp"
	l, err := coapNet.NewTCPListener(network, laddr, time.Millisecond*100)
	if err != nil {
		return nil, "", nil, fmt.Errorf("cannot create new tls listener: %v", err)
	}

	server := &Server{Listener: l, ReadTimeout: time.Second * 3600, WriteTimeout: time.Second * 3600,
		NotifySessionNewFunc: func(s *ClientConn) {
			fmt.Printf("networkSession start %v\n", s.RemoteAddr())
		}, NotifySessionEndFunc: func(w *ClientConn, err error) {
			fmt.Printf("networkSession end %v: %v\n", w.RemoteAddr(), err)
		}, Handler: handler,
		BlockWiseTransfer:    &BlockWiseTransfer,
		BlockWiseTransferSzx: &BlockWiseTransferSzx,
		MaxMessageSize:       ^uint32(0),
	}

	waitLock := sync.Mutex{}
	waitLock.Lock()
	server.NotifyStartedFunc = waitLock.Unlock

	// See the comment in RunLocalUDPServerWithFinChan as to
	// why fin must be buffered.
	fin := make(chan error, 1)

	go func() {
		fin <- server.ActivateAndServe()
		l.Close()
	}()

	waitLock.Lock()
	return server, l.Addr().String(), fin, nil
}

func RunLocalTCPServer(laddr string, BlockWiseTransfer bool, BlockWiseTransferSzx BlockWiseSzx) (*Server, string, chan error, error) {
	return RunLocalServerTCPWithHandler(laddr, BlockWiseTransfer, BlockWiseTransferSzx, nil)
}

func RunLocalTLSServer(laddr string, config *tls.Config) (*Server, string, chan error, error) {
	l, err := coapNet.NewTLSListener("tcp", laddr, config, time.Millisecond*100)
	if err != nil {
		return nil, "", nil, err
	}

	server := &Server{Listener: l, ReadTimeout: time.Hour, WriteTimeout: time.Hour,
		NotifySessionNewFunc: func(s *ClientConn) {
			fmt.Printf("networkSession start %v\n", s.RemoteAddr())
		}, NotifySessionEndFunc: func(w *ClientConn, err error) {
			fmt.Printf("networkSession end %v: %v\n", w.RemoteAddr(), err)
		},
		MaxMessageSize: ^uint32(0),
	}

	// fin must be buffered so the goroutine below won't block
	// forever if fin is never read from. This always happens
	// in RunLocalUDPServer and can happen in TestShutdownUDP.
	fin := make(chan error, 1)

	go func() {
		fin <- server.ActivateAndServe()
		l.Close()
	}()

	return server, l.Addr().String(), fin, nil
}

func RunLocalDTLSServer(laddr string, config *dtls.Config, BlockWiseTransfer bool, BlockWiseTransferSzx BlockWiseSzx) (*Server, string, chan error, error) {
	l, err := coapNet.NewDTLSListener("udp", laddr, config, time.Millisecond*100)
	if err != nil {
		return nil, "", nil, err
	}

	server := &Server{Listener: l, ReadTimeout: time.Hour, WriteTimeout: time.Hour,
		BlockWiseTransfer:    &BlockWiseTransfer,
		BlockWiseTransferSzx: &BlockWiseTransferSzx,
		NotifySessionNewFunc: func(s *ClientConn) {
			fmt.Printf("networkSession start %v\n", s.RemoteAddr())
		}, NotifySessionEndFunc: func(w *ClientConn, err error) {
			fmt.Printf("networkSession end %v: %v\n", w.RemoteAddr(), err)
		},
		MaxMessageSize: ^uint32(0),
	}

	// fin must be buffered so the goroutine below won't block
	// forever if fin is never read from. This always happens
	// in RunLocalUDPServer and can happen in TestShutdownUDP.
	fin := make(chan error, 1)

	go func() {
		fin <- server.ActivateAndServe()
		l.Close()
	}()

	return server, l.Addr().String(), fin, nil
}

type clientHandler func(t *testing.T, payload []byte, co *ClientConn)

func testServingTCPWithMsgWithObserver(t *testing.T, net string, BlockWiseTransfer bool, BlockWiseTransferSzx BlockWiseSzx, payload []byte, ch clientHandler, observeFunc HandlerFunc) {
	HandleFunc("/test", EchoServer)
	defer HandleRemove("/test")

	var s *Server
	var addrstr string
	var err error
	c := &Client{
		Net:                  net,
		Handler:              observeFunc,
		BlockWiseTransfer:    &BlockWiseTransfer,
		BlockWiseTransferSzx: &BlockWiseTransferSzx,
		MaxMessageSize:       ^uint32(0),
	}
	var fin chan error
	switch net {
	case "tcp", "tcp4", "tcp6":
		s, addrstr, fin, err = RunLocalTCPServer(":0", BlockWiseTransfer, BlockWiseTransferSzx)
	case "udp", "udp4", "udp6":
		s, addrstr, fin, err = RunLocalUDPServer(net, ":0", BlockWiseTransfer, BlockWiseTransferSzx)
	case "udp-dtls", "udp4-dtls", "udp6-dtls":
		config := &dtls.Config{
			PSK: func(hint []byte) ([]byte, error) {
				fmt.Printf("Client's hint: %s \n", hint)
				return []byte{0xAB, 0xC1, 0x23}, nil
			},
			PSKIdentityHint: []byte("Pion DTLS Client"),
			CipherSuites:    []dtls.CipherSuiteID{dtls.TLS_PSK_WITH_AES_128_CCM_8},
		}
		c.DTLSConfig = &dtls.Config{
			PSK: func(hint []byte) ([]byte, error) {
				fmt.Printf("Server's hint: %s \n", hint)
				return []byte{0xAB, 0xC1, 0x23}, nil
			},
			PSKIdentityHint: []byte("Pion DTLS Server"),
			CipherSuites:    []dtls.CipherSuiteID{dtls.TLS_PSK_WITH_AES_128_CCM_8},
		}
		s, addrstr, fin, err = RunLocalDTLSServer(":0", config, BlockWiseTransfer, BlockWiseTransferSzx)
	case "tcp-tls", "tcp4-tls", "tcp6-tls":
		cert, err := tls.X509KeyPair(CertPEMBlock, KeyPEMBlock)
		if err != nil {
			t.Fatalf("unable to build certificate: %v", err)
		}
		config := tls.Config{
			Certificates: []tls.Certificate{cert},
		}
		s, addrstr, fin, err = RunLocalTLSServer(":0", &config)
		if err != nil {
			t.Fatalf("unable to run test server: %v", err)
		}
		c.TLSConfig = &tls.Config{InsecureSkipVerify: true}
	}

	if err != nil {
		t.Fatalf("unable to run test server: %v", err)
	}
	defer func() {
		s.Shutdown()
		<-fin
	}()

	co, err := c.Dial(addrstr)
	if err != nil {
		t.Fatalf("unable to dialing: %v", err)
	}
	defer co.Close()

	ch(t, payload, co)
}

func simpleMsgToPath(t *testing.T, payload []byte, co *ClientConn, path string) {
	req, err := co.NewPostRequest(path, TextPlain, bytes.NewBuffer(payload))
	if err != nil {
		t.Fatal("cannot create request", err)
	}

	res := CreateRespMessageByReq(co.commander.networkSession.IsTCP(), codes.Valid, req)

	m, err := co.Exchange(req)
	if err != nil {
		t.Fatal("failed to exchange", err)
	}
	if m == nil {
		t.Fatalf("Didn't receive CoAP response")
	}
	assertEqualMessages(t, res, m)
}

func simpleMsg(t *testing.T, payload []byte, co *ClientConn) {
	simpleMsgToPath(t, payload, co, "/test")
}

func pingMsg(t *testing.T, payload []byte, co *ClientConn) {
	err := co.Ping(3600 * time.Second)
	if err != nil {
		t.Fatal("failed to exchange", err)
	}
}

func testServingTCPWithMsg(t *testing.T, net string, BlockWiseTransfer bool, BlockWiseTransferSzx BlockWiseSzx, payload []byte, ch clientHandler) {
	testServingTCPWithMsgWithObserver(t, net, BlockWiseTransfer, BlockWiseTransferSzx, payload, ch, nil)
}

func TestServingUDP(t *testing.T) {
	testServingTCPWithMsg(t, "udp", false, BlockWiseSzx16, make([]byte, 128), simpleMsg)
}

func TestServingUDPPing(t *testing.T) {

	testServingTCPWithMsg(t, "udp", false, BlockWiseSzx16, nil, pingMsg)
}

func TestServingTCPPing(t *testing.T) {
	testServingTCPWithMsg(t, "tcp", false, BlockWiseSzx16, nil, pingMsg)
}

func TestServingUDPBigMsg(t *testing.T) {
	testServingTCPWithMsg(t, "udp", false, BlockWiseSzx16, make([]byte, 1024), simpleMsg)
}

func TestServingTCP(t *testing.T) {
	testServingTCPWithMsg(t, "tcp", false, BlockWiseSzx16, make([]byte, 128), simpleMsg)
}

func TestServingTCPBigMsg(t *testing.T) {
	testServingTCPWithMsg(t, "tcp", false, BlockWiseSzx16, make([]byte, 10*1024*1024), simpleMsg)
}

func TestServingTLS(t *testing.T) {
	testServingTCPWithMsg(t, "tcp-tls", false, BlockWiseSzx16, make([]byte, 128), simpleMsg)
}

func TestServingTLSBigMsg(t *testing.T) {
	testServingTCPWithMsg(t, "tcp-tls", false, BlockWiseSzx16, make([]byte, 10*1024*1024), simpleMsg)
}

func ChallegingServer(w ResponseWriter, r *Request) {
	_, err := r.Client.Post("/test", TextPlain, bytes.NewBuffer([]byte("hello, world!")))
	if err != nil {
		panic(err.Error())
	}

	w.WriteMsg(CreateRespMessageByReq(r.Client.networkSession().IsTCP(), codes.Valid, r.Msg))
}

func ChallegingServerTimeout(w ResponseWriter, r *Request) {
	req := r.Client.NewMessage(MessageParams{
		Type:      Confirmable,
		Code:      codes.GET,
		MessageID: 12345,
		Payload:   []byte("hello, world!"),
		Token:     []byte("abcd"),
	})
	req.SetOption(ContentFormat, TextPlain)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := r.Client.networkSession().ExchangeWithContext(ctx, req)
	if err == nil {
		panic("Error: expected timeout")
	}

	w.WriteMsg(CreateRespMessageByReq(r.Client.networkSession().IsTCP(), codes.Valid, r.Msg))
}

func simpleChallengingMsg(t *testing.T, payload []byte, co *ClientConn) {
	simpleChallengingPathMsg(t, payload, co, "/challenging", false)
}

func simpleChallengingTimeoutMsg(t *testing.T, payload []byte, co *ClientConn) {
	simpleChallengingPathMsg(t, payload, co, "/challengingTimeout", true)
}

func simpleChallengingPathMsg(t *testing.T, payload []byte, co *ClientConn, path string, testTimeout bool) {
	req0 := co.NewMessage(MessageParams{
		Type:      Confirmable,
		Code:      codes.POST,
		MessageID: 1234,
		Payload:   payload,
		Token:     []byte("chall"),
	},
	)
	req0.SetOption(ContentFormat, TextPlain)
	req0.SetPathString(path)

	resp0, err := co.Exchange(req0)
	if err != nil {
		t.Fatalf("unable to read msg from server: %v", err)
	}

	res := CreateRespMessageByReq(co.commander.networkSession.IsTCP(), codes.Valid, req0)
	assertEqualMessages(t, res, resp0)
}

func TestServingChallengingClient(t *testing.T) {
	HandleFunc("/challenging", ChallegingServer)
	defer HandleRemove("/challenging")
	testServingTCPWithMsg(t, "udp", false, BlockWiseSzx16, make([]byte, 128), simpleChallengingMsg)
}

func TestServingChallengingClientTCP(t *testing.T) {
	HandleFunc("/challenging", ChallegingServer)
	defer HandleRemove("/challenging")
	testServingTCPWithMsg(t, "tcp", false, BlockWiseSzx16, make([]byte, 128), simpleChallengingMsg)
}

func TestServingChallengingClientTLS(t *testing.T) {
	HandleFunc("/challenging", ChallegingServer)
	defer HandleRemove("/challenging")
	testServingTCPWithMsg(t, "tcp-tls", false, BlockWiseSzx16, make([]byte, 128), simpleChallengingMsg)
}

func TestServingChallengingTimeoutClient(t *testing.T) {
	HandleFunc("/challengingTimeout", ChallegingServerTimeout)
	defer HandleRemove("/challengingTimeout")
	testServingTCPWithMsgWithObserver(t, "udp", false, BlockWiseSzx16, make([]byte, 128), simpleChallengingTimeoutMsg, func(w ResponseWriter, r *Request) {
		//for timeout
		time.Sleep(2 * time.Second)
	})
}

func TestServingChallengingTimeoutClientTCP(t *testing.T) {
	HandleFunc("/challengingTimeout", ChallegingServerTimeout)
	defer HandleRemove("/challengingTimeout")
	testServingTCPWithMsgWithObserver(t, "tcp", false, BlockWiseSzx16, make([]byte, 128), simpleChallengingTimeoutMsg, func(w ResponseWriter, r *Request) {
		//for timeout
		time.Sleep(2 * time.Second)
	})
}

func TestServingChallengingTimeoutClientTLS(t *testing.T) {
	HandleFunc("/challengingTimeout", ChallegingServerTimeout)
	defer HandleRemove("/challengingTimeout")
	testServingTCPWithMsgWithObserver(t, "tcp-tls", false, BlockWiseSzx16, make([]byte, 128), simpleChallengingTimeoutMsg, func(w ResponseWriter, r *Request) {
		//for timeout
		time.Sleep(2 * time.Second)
	})
}

func testServingMCast(t *testing.T, lnet, laddr string, BlockWiseTransfer bool, BlockWiseTransferSzx BlockWiseSzx, payloadLen int) {
	testServingMCastWithIfaces(t, lnet, laddr, BlockWiseTransfer, BlockWiseTransferSzx, payloadLen, nil)
}

func testServingMCastWithIfaces(t *testing.T, lnet, laddr string, BlockWiseTransfer bool, BlockWiseTransferSzx BlockWiseSzx, payloadLen int, ifaces []net.Interface) {
	addrMcast := laddr
	ansArrived := make(chan bool)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	responseServerConn := make([]*ClientConn, 0)
	var lockResponseServerConn sync.Mutex
	responseServer := Client{
		Net:                  strings.TrimSuffix(lnet, "-mcast"),
		BlockWiseTransfer:    &BlockWiseTransfer,
		BlockWiseTransferSzx: &BlockWiseTransferSzx,
		Handler: func(w ResponseWriter, r *Request) {
			t.Log("responseServer.Handler")
			resp := w.NewResponse(codes.Content)
			resp.SetPayload(make([]byte, payloadLen))
			resp.SetOption(ContentFormat, TextPlain)
			err := w.WriteMsg(resp)
			if err != nil {
				t.Fatalf("cannot send response: %v", err)
			}
		},
	}

	s, _, fin, err := RunLocalServerUDPWithHandlerIfaces(lnet, addrMcast, BlockWiseTransfer, BlockWiseTransferSzx, func(w ResponseWriter, r *Request) {
		t.Log("RunLocalServerUDPWithHandler.Handler")
		resp := w.NewResponse(codes.Content)
		resp.SetPayload(make([]byte, payloadLen))
		resp.SetOption(ContentFormat, TextPlain)
		conn, err := responseServer.Dial(r.Client.RemoteAddr().String())
		if err != nil {
			t.Fatalf("cannot create connection %v", err)
		}
		err = conn.WriteMsg(resp)
		if err != nil {
			t.Fatalf("cannot send response %v", err)
		}
		lockResponseServerConn.Lock()
		responseServerConn = append(responseServerConn, conn)
		lockResponseServerConn.Unlock()
	}, ifaces)
	if err != nil {
		t.Fatalf("unable to run test server: %v", err)
	}
	defer func() {
		s.Shutdown()
		lockResponseServerConn.Lock()
		for _, conn := range responseServerConn {
			conn.Close()
		}
		lockResponseServerConn.Unlock()
		<-fin
	}()

	c := MulticastClient{
		Net:                  strings.TrimSuffix(lnet, "-mcast"),
		BlockWiseTransfer:    &BlockWiseTransfer,
		BlockWiseTransferSzx: &BlockWiseTransferSzx,
		Handler: func(w ResponseWriter, r *Request) {
			t.Log("MulticastClient default handler")
		},
	}

	co, err := c.Dial(addrMcast)
	if err != nil {
		t.Fatalf("unable to dialing: %v", err)
	}
	defer co.Close()

	rp, err := co.Publish("/test", func(req *Request) {
		ansArrived <- true
	})
	if err != nil {
		t.Fatalf("unable to publishing: %v", err)
	}
	defer rp.Cancel()

	select {
	case <-ansArrived:
	case <-ctx.Done():
		t.Fatalf("timeout: %v", ctx.Err())
	}
}

func TestServingIPv4MCast(t *testing.T) {
	testServingMCast(t, "udp4-mcast", "225.0.1.187:11111", false, BlockWiseSzx16, 16)
}

func TestServingIPv6MCast(t *testing.T) {
	testServingMCast(t, "udp6-mcast", "[ff03::158]:11111", false, BlockWiseSzx16, 16)
}

func TestServingRootPath(t *testing.T) {
	HandleFunc("/", EchoServer)
	defer HandleRemove("/")
	testServingTCPWithMsg(t, "udp", false, BlockWiseSzx16, make([]byte, 128), func(t *testing.T, payload []byte, co *ClientConn) {
		simpleMsgToPath(t, payload, co, "/")
	})
	testServingTCPWithMsg(t, "udp", false, BlockWiseSzx16, make([]byte, 128), func(t *testing.T, payload []byte, co *ClientConn) {
		simpleMsgToPath(t, payload, co, "")
	})
}

func TestServingEmptyRootPath(t *testing.T) {
	HandleFunc("", EchoServer)
	defer HandleRemove("")
	testServingTCPWithMsg(t, "udp", false, BlockWiseSzx16, make([]byte, 128), func(t *testing.T, payload []byte, co *ClientConn) {
		simpleMsgToPath(t, payload, co, "/")
	})
	testServingTCPWithMsg(t, "udp", false, BlockWiseSzx16, make([]byte, 128), func(t *testing.T, payload []byte, co *ClientConn) {
		simpleMsgToPath(t, payload, co, "")
	})
}

type dataReader struct {
	data   []byte
	offset int
}

func (r *dataReader) Read(p []byte) (n int, err error) {
	l := len(p)
	if (len(r.data) - r.offset) < l {
		l = len(r.data) - r.offset
	}
	if l != 0 {
		copy(p, r.data[r.offset:r.offset+l])
		r.offset += l
	} else {
		return 0, io.EOF
	}
	return l, nil
}

func BenchmarkServe(b *testing.B) {
	b.StopTimer()
	HandleFunc("/test", EchoServer)
	defer HandleRemove("/test")

	BlockWiseTransfer := false
	BlockWiseTransferSzx := BlockWiseSzx1024

	s, addrstr, fin, err := RunLocalUDPServer("udp", ":0", BlockWiseTransfer, BlockWiseTransferSzx)
	if err != nil {
		b.Fatalf("unable to run test server: %v", err)
	}
	defer func() {
		s.Shutdown()
		<-fin
	}()

	client := Client{
		Net:                  "udp",
		BlockWiseTransfer:    &BlockWiseTransfer,
		BlockWiseTransferSzx: &BlockWiseTransferSzx,
		MaxMessageSize:       ^uint32(0),
	}
	co, err := client.Dial(addrstr)
	if err != nil {
		b.Fatalf("unable to dialing: %v", err)
	}
	defer co.Close()

	data := []byte("Content sent by client")
	b.StartTimer()
	for i := uint32(0); i < uint32(b.N); i++ {
		_, err := co.Post("/test", TextPlain, &dataReader{data: data})
		if err != nil {
			b.Fatalf("unable to read msg from server: %v", err)
		}
	}
}

func BenchmarkServeBlockWise(b *testing.B) {
	b.StopTimer()
	HandleFunc("/test", EchoServer)
	defer HandleRemove("/test")

	BlockWiseTransfer := true
	BlockWiseTransferSzx := BlockWiseSzx16

	s, addrstr, fin, err := RunLocalUDPServer("udp", ":0", BlockWiseTransfer, BlockWiseSzx1024)
	if err != nil {
		b.Fatalf("unable to run test server: %v", err)
	}
	defer func() {
		s.Shutdown()
		<-fin
	}()

	client := Client{
		Net:                  "udp",
		BlockWiseTransfer:    &BlockWiseTransfer,
		BlockWiseTransferSzx: &BlockWiseTransferSzx,
		MaxMessageSize:       ^uint32(0),
	}
	co, err := client.Dial(addrstr)
	if err != nil {
		b.Fatalf("unable to dialing: %v", err)
	}
	defer co.Close()

	data := make([]byte, 128)
	b.StartTimer()
	for i := uint32(0); i < uint32(b.N); i++ {
		_, err := co.Post("/test", TextPlain, &dataReader{data: data})
		if err != nil {
			b.Fatalf("unable to read msg from server: %v", err)
		}
	}
}

func BenchmarkServeTCP(b *testing.B) {
	b.StopTimer()
	HandleFunc("/test", EchoServer)
	defer HandleRemove("/test")

	s, addrstr, fin, err := RunLocalTCPServer(":0", false, BlockWiseSzx16)
	if err != nil {
		b.Fatalf("unable to run test server: %v", err)
	}
	defer func() {
		s.Shutdown()
		<-fin
	}()

	co, err := Dial("tcp", addrstr)
	if err != nil {
		b.Fatalf("unable dialing: %v", err)
	}
	defer co.Close()

	data := make([]byte, ^uint16(0))
	b.StartTimer()
	for i := uint32(0); i < uint32(b.N); i++ {
		_, err := co.Post("/test", TextPlain, &dataReader{data: data})
		if err != nil {
			b.Fatalf("unable to read msg from server: %v", err)
		}
	}
}

func BenchmarkServeTCPBlockwise(b *testing.B) {
	b.StopTimer()
	HandleFunc("/test", EchoServer)
	defer HandleRemove("/test")

	BlockWiseTransfer := true
	BlockWiseTransferSzx := BlockWiseSzxBERT

	s, addrstr, fin, err := RunLocalTCPServer(":0", BlockWiseTransfer, BlockWiseTransferSzx)
	if err != nil {
		b.Fatalf("unable to run test server: %v", err)
	}
	defer func() {
		s.Shutdown()
		<-fin
	}()

	client := Client{
		Net:                  "tcp",
		BlockWiseTransfer:    &BlockWiseTransfer,
		BlockWiseTransferSzx: &BlockWiseTransferSzx,
		MaxMessageSize:       ^uint32(0),
	}
	co, err := client.Dial(addrstr)
	if err != nil {
		b.Fatalf("unable dialing: %v", err)
	}
	defer co.Close()

	data := make([]byte, ^uint16(0))
	b.StartTimer()
	for i := uint32(0); i < uint32(b.N); i++ {
		_, err := co.Post("/test", TextPlain, &dataReader{data: data})
		if err != nil {
			b.Fatalf("unable to read msg from server: %v", err)
		}
	}
}

func benchmarkServeTCPStreamWithMsg(b *testing.B, req *TcpMessage) {
	b.StopTimer()
	HandleFunc("/test", EchoServer)
	defer HandleRemove("/test")

	s, addrstr, fin, err := RunLocalTCPServer(":0", false, BlockWiseSzx16)
	if err != nil {
		b.Fatalf("unable to run test server: %v", err)
	}
	defer func() {
		s.Shutdown()
		<-fin
	}()

	co, err := Dial("tcp", addrstr)
	if err != nil {
		b.Fatalf("unable dialing: %v", err)
	}
	defer co.Close()
	res := CreateRespMessageByReq(true, codes.Valid, req)

	b.StartTimer()
	sync := make(chan bool)

	for i := uint32(0); i < uint32(b.N); i++ {
		go func(t uint32) {
			abc := *req
			token := make([]byte, 8)
			binary.LittleEndian.PutUint32(token, t)
			abc.SetToken(token)
			resp, err := co.Exchange(&abc)
			if err != nil {
				b.Fatalf("unable to read msg from server: %v", err)
			}
			if !bytes.Equal(resp.Payload(), res.Payload()) {
				b.Fatalf("bad payload: %v", err)
			}
			sync <- true
		}(i)
	}

	for i := 0; i < b.N; i++ {
		<-sync
	}

	b.StopTimer()
}

func BenchmarkServeTCPStream(b *testing.B) {
	req := &TcpMessage{
		MessageBase{
			typ:     Confirmable,
			code:    codes.POST,
			payload: []byte("Content sent by client"),
		},
	}
	req.SetOption(ContentFormat, TextPlain)
	req.SetPathString("/test")
	benchmarkServeTCPStreamWithMsg(b, req)
}

func BenchmarkServeTCPStreamBigMsg(b *testing.B) {
	req := &TcpMessage{
		MessageBase: MessageBase{
			typ:     Confirmable,
			code:    codes.POST,
			payload: make([]byte, 1024*1024*10),
		},
	}
	req.SetOption(ContentFormat, TextPlain)
	req.SetPathString("/test")
	benchmarkServeTCPStreamWithMsg(b, req)
}

var (
	// CertPEMBlock is a X509 data used to test TLS servers (used with tls.X509KeyPair)
	CertPEMBlock = []byte(`-----BEGIN CERTIFICATE-----
MIIDAzCCAeugAwIBAgIRAJFYMkcn+b8dpU15wjf++GgwDQYJKoZIhvcNAQELBQAw
EjEQMA4GA1UEChMHQWNtZSBDbzAeFw0xNjAxMDgxMjAzNTNaFw0xNzAxMDcxMjAz
NTNaMBIxEDAOBgNVBAoTB0FjbWUgQ28wggEiMA0GCSqGSIb3DQEBAQUAA4IBDwAw
ggEKAoIBAQDXjqO6skvP03k58CNjQggd9G/mt+Wa+xRU+WXiKCCHttawM8x+slq5
yfsHCwxlwsGn79HmJqecNqgHb2GWBXAvVVokFDTcC1hUP4+gp2gu9Ny27UHTjlLm
O0l/xZ5MN8tfKyYlFw18tXu3fkaPyHj8v/D1RDkuo4ARdFvGSe8TqisbhLk2+9ow
xfIGbEM9Fdiw8qByC2+d+FfvzIKz3GfQVwn0VoRom8L6NBIANq1IGrB5JefZB6nv
DnfuxkBmY7F1513HKuEJ8KsLWWZWV9OPU4j4I4Rt+WJNlKjbD2srHxyrS2RDsr91
8nCkNoWVNO3sZq0XkWKecdc921vL4ginAgMBAAGjVDBSMA4GA1UdDwEB/wQEAwIC
pDATBgNVHSUEDDAKBggrBgEFBQcDATAPBgNVHRMBAf8EBTADAQH/MBoGA1UdEQQT
MBGCCWxvY2FsaG9zdIcEfwAAATANBgkqhkiG9w0BAQsFAAOCAQEAGcU3iyLBIVZj
aDzSvEDHUd1bnLBl1C58Xu/CyKlPqVU7mLfK0JcgEaYQTSX6fCJVNLbbCrcGLsPJ
fbjlBbyeLjTV413fxPVuona62pBFjqdtbli2Qe8FRH2KBdm41JUJGdo+SdsFu7nc
BFOcubdw6LLIXvsTvwndKcHWx1rMX709QU1Vn1GAIsbJV/DWI231Jyyb+lxAUx/C
8vce5uVxiKcGS+g6OjsN3D3TtiEQGSXLh013W6Wsih8td8yMCMZ3w8LQ38br1GUe
ahLIgUJ9l6HDguM17R7kGqxNvbElsMUHfTtXXP7UDQUiYXDakg8xDP6n9DCDhJ8Y
bSt7OLB7NQ==
-----END CERTIFICATE-----`)

	// KeyPEMBlock is a X509 data used to test TLS servers (used with tls.X509KeyPair)
	KeyPEMBlock = []byte(`-----BEGIN RSA PRIVATE KEY-----
MIIEpQIBAAKCAQEA146jurJLz9N5OfAjY0IIHfRv5rflmvsUVPll4iggh7bWsDPM
frJaucn7BwsMZcLBp+/R5iannDaoB29hlgVwL1VaJBQ03AtYVD+PoKdoLvTctu1B
045S5jtJf8WeTDfLXysmJRcNfLV7t35Gj8h4/L/w9UQ5LqOAEXRbxknvE6orG4S5
NvvaMMXyBmxDPRXYsPKgcgtvnfhX78yCs9xn0FcJ9FaEaJvC+jQSADatSBqweSXn
2Qep7w537sZAZmOxdeddxyrhCfCrC1lmVlfTj1OI+COEbfliTZSo2w9rKx8cq0tk
Q7K/dfJwpDaFlTTt7GatF5FinnHXPdtby+IIpwIDAQABAoIBAAJK4RDmPooqTJrC
JA41MJLo+5uvjwCT9QZmVKAQHzByUFw1YNJkITTiognUI0CdzqNzmH7jIFs39ZeG
proKusO2G6xQjrNcZ4cV2fgyb5g4QHStl0qhs94A+WojduiGm2IaumAgm6Mc5wDv
ld6HmknN3Mku/ZCyanVFEIjOVn2WB7ZQLTBs6ZYaebTJG2Xv6p9t2YJW7pPQ9Xce
s9ohAWohyM4X/OvfnfnLtQp2YLw/BxwehBsCR5SXM3ibTKpFNtxJC8hIfTuWtxZu
2ywrmXShYBRB1WgtZt5k04bY/HFncvvcHK3YfI1+w4URKtwdaQgPUQRbVwDwuyBn
flfkCJECgYEA/eWt01iEyE/lXkGn6V9lCocUU7lCU6yk5UT8VXVUc5If4KZKPfCk
p4zJDOqwn2eM673aWz/mG9mtvAvmnugaGjcaVCyXOp/D/GDmKSoYcvW5B/yjfkLy
dK6Yaa5LDRVYlYgyzcdCT5/9Qc626NzFwKCZNI4ncIU8g7ViATRxWJ8CgYEA2Ver
vZ0M606sfgC0H3NtwNBxmuJ+lIF5LNp/wDi07lDfxRR1rnZMX5dnxjcpDr/zvm8J
WtJJX3xMgqjtHuWKL3yKKony9J5ZPjichSbSbhrzfovgYIRZLxLLDy4MP9L3+CX/
yBXnqMWuSnFX+M5fVGxdDWiYF3V+wmeOv9JvavkCgYEAiXAPDFzaY+R78O3xiu7M
r0o3wqqCMPE/wav6O/hrYrQy9VSO08C0IM6g9pEEUwWmzuXSkZqhYWoQFb8Lc/GI
T7CMXAxXQLDDUpbRgG79FR3Wr3AewHZU8LyiXHKwxcBMV4WGmsXGK3wbh8fyU1NO
6NsGk+BvkQVOoK1LBAPzZ1kCgYEAsBSmD8U33T9s4dxiEYTrqyV0lH3g/SFz8ZHH
pAyNEPI2iC1ONhyjPWKlcWHpAokiyOqeUpVBWnmSZtzC1qAydsxYB6ShT+sl9BHb
RMix/QAauzBJhQhUVJ3OIys0Q1UBDmqCsjCE8SfOT4NKOUnA093C+YT+iyrmmktZ
zDCJkckCgYEAndqM5KXGk5xYo+MAA1paZcbTUXwaWwjLU+XSRSSoyBEi5xMtfvUb
7+a1OMhLwWbuz+pl64wFKrbSUyimMOYQpjVE/1vk/kb99pxbgol27hdKyTH1d+ov
kFsxKCqxAnBVGEWAvVZAiiTOxleQFjz5RnL0BQp9Lg2cQe+dvuUmIAA=
-----END RSA PRIVATE KEY-----`)
)
