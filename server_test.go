package coap

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

func CreateRespMessageByReq(isTCP bool, code COAPCode, req Message) Message {
	if isTCP {
		resp := &TcpMessage{
			MessageBase{
				//typ:       Acknowledgement, elided by COAP over TCP
				code: Valid,
				//messageID: req.MessageID(), , elided by COAP over TCP
				payload: req.Payload(),
				token:   req.Token(),
			},
		}
		resp.SetPath(req.Path())
		resp.SetOption(ContentFormat, req.Option(ContentFormat))
		return resp
	}
	resp := &DgramMessage{
		MessageBase{
			typ:       Acknowledgement,
			code:      Valid,
			messageID: req.MessageID(),
			payload:   req.Payload(),
			token:     req.Token(),
		},
	}
	resp.SetPath(req.Path())
	resp.SetOption(ContentFormat, req.Option(ContentFormat))
	return resp
}

func EchoServer(w Session, req Message) {
	if req.IsConfirmable() {
		w.WriteMsg(CreateRespMessageByReq(w.IsTCP(), Valid, req))
	}
}

func EchoServerBadID(w Session, req Message) {
	if req.IsConfirmable() {
		w.WriteMsg(CreateRespMessageByReq(w.IsTCP(), BadRequest, req))
	}
}

func RunLocalServerUDPWithHandler(laddr string, handler HandlerFunc) (*Server, string, chan error, error) {
	a, err := net.ResolveUDPAddr("udp", laddr)
	if err != nil {
		return nil, "", nil, err
	}
	pc, err := net.ListenUDP("udp", a)
	if err != nil {
		return nil, "", nil, err
	}

	server := &Server{Conn: pc, ReadTimeout: time.Hour, WriteTimeout: time.Hour,
		CreateSessionUDPFunc: func(connection conn, srv *Server, sessionUDPData *SessionUDPData) Session {
			w := NewSessionUDP(connection, srv, sessionUDPData)
			fmt.Printf("Session start %v\n", w.RemoteAddr())
			return w
		}, NotifySessionEndFunc: func(w Session, err error) {
			fmt.Printf("Session end %v: %v\n", w.RemoteAddr(), err)
		}, Handler: handler}

	waitLock := sync.Mutex{}
	waitLock.Lock()
	server.NotifyStartedFunc = waitLock.Unlock

	// fin must be buffered so the goroutine below won't block
	// forever if fin is never read from. This always happens
	// in RunLocalUDPServer and can happen in TestShutdownUDP.
	fin := make(chan error, 1)

	go func() {
		fin <- server.ActivateAndServe()
		pc.Close()
	}()

	waitLock.Lock()
	return server, pc.LocalAddr().String(), fin, nil
}

func RunLocalUDPServer(laddr string) (*Server, string, chan error, error) {
	return RunLocalServerUDPWithHandler(laddr, nil)
}

func RunLocalServerTCPWithHandler(laddr string, handler HandlerFunc) (*Server, string, chan error, error) {
	l, err := net.Listen("tcp", laddr)
	if err != nil {
		return nil, "", nil, err
	}

	server := &Server{Listener: l, ReadTimeout: time.Second * 3600, WriteTimeout: time.Second * 3600,
		CreateSessionTCPFunc: func(connection conn, srv *Server) Session {
			w := NewSessionTCP(connection, srv)
			fmt.Printf("Session start %v\n", w.RemoteAddr())
			return w
		}, NotifySessionEndFunc: func(w Session, err error) {
			fmt.Printf("Session end %v: %v\n", w.RemoteAddr(), err)
		}, Handler: handler}

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

func RunLocalTCPServer(laddr string) (*Server, string, chan error, error) {
	return RunLocalServerTCPWithHandler(laddr, nil)
}

func RunLocalTLSServer(laddr string, config *tls.Config) (*Server, string, chan error, error) {
	l, err := tls.Listen("tcp", laddr, config)
	if err != nil {
		return nil, "", nil, err
	}

	server := &Server{Listener: l, ReadTimeout: time.Hour, WriteTimeout: time.Hour,
		CreateSessionTCPFunc: func(connection conn, srv *Server) Session {
			w := NewSessionTCP(connection, srv)
			fmt.Printf("Session start %v\n", w.RemoteAddr())
			return w
		}, NotifySessionEndFunc: func(w Session, err error) {
			fmt.Printf("Session end %v: %v\n", w.RemoteAddr(), err)
		}}

	waitLock := sync.Mutex{}
	waitLock.Lock()
	server.NotifyStartedFunc = waitLock.Unlock

	// fin must be buffered so the goroutine below won't block
	// forever if fin is never read from. This always happens
	// in RunLocalUDPServer and can happen in TestShutdownUDP.
	fin := make(chan error, 1)

	go func() {
		fin <- server.ActivateAndServe()
		l.Close()
	}()

	waitLock.Lock()
	return server, l.Addr().String(), fin, nil
}

type clientHandler func(t *testing.T, payload []byte, co *ClientConn)

func testServingTCPwithMsg(t *testing.T, net string, payload []byte, ch clientHandler) {
	HandleFunc("/test", EchoServer)
	defer HandleRemove("/test")

	var s *Server
	var addrstr string
	var err error
	c := new(Client)
	c.Net = net
	var fin chan error
	switch net {
	case "tcp", "tcp4", "tcp6":
		s, addrstr, fin, err = RunLocalTCPServer(":0")
	case "udp", "udp4", "udp6":
		s, addrstr, fin, err = RunLocalUDPServer(":0")
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

func simpleMsg(t *testing.T, payload []byte, co *ClientConn) {
	req := co.NewMessage(MessageParams{
		Type:      Confirmable,
		Code:      POST,
		MessageID: 1234,
		Payload:   payload,
		Token:     []byte("abcd"),
	},
	)
	req.SetOption(ContentFormat, TextPlain)
	req.SetPathString("/test")

	res := CreateRespMessageByReq(co.session.IsTCP(), Valid, req)

	m, err := co.Exchange(req, 1*time.Second)
	if err != nil {
		t.Fatal("failed to exchange", err)
	}
	if m == nil {
		t.Fatalf("Didn't receive CoAP response")
	}
	assertEqualMessages(t, res, m)
}

func TestServingUDP(t *testing.T) {
	testServingTCPwithMsg(t, "udp", make([]byte, 128), simpleMsg)
}

func TestServingUDPBigMsg(t *testing.T) {
	testServingTCPwithMsg(t, "udp", make([]byte, 1300), simpleMsg)
}

func TestServingTCP(t *testing.T) {
	testServingTCPwithMsg(t, "tcp", make([]byte, 128), simpleMsg)
}

func TestServingTCPBigMsg(t *testing.T) {
	testServingTCPwithMsg(t, "tcp", make([]byte, 10*1024*1024), simpleMsg)
}

func TestServingTLS(t *testing.T) {
	testServingTCPwithMsg(t, "tcp-tls", make([]byte, 128), simpleMsg)
}

func TestServingTLSBigMsg(t *testing.T) {
	testServingTCPwithMsg(t, "tcp-tls", make([]byte, 10*1024*1024), simpleMsg)
}

func ChallegingServer(w Session, req Message) {
	r := w.NewMessage(MessageParams{
		Type:      Confirmable,
		Code:      GET,
		MessageID: 12345,
		Payload:   []byte("hello, world!"),
		Token:     []byte("abcd"),
	})
	_, err := w.Exchange(r, time.Second*3)
	if err != nil {
		panic(err.Error())
	}

	w.WriteMsg(CreateRespMessageByReq(w.IsTCP(), Valid, req))
}

func ChallegingServerTimeout(w Session, req Message) {
	r := w.NewMessage(MessageParams{
		Type:      Confirmable,
		Code:      GET,
		MessageID: 12345,
		Payload:   []byte("hello, world!"),
		Token:     []byte("abcd"),
	})
	r.SetOption(ContentFormat, TextPlain)
	_, err := w.Exchange(r, time.Second*0)
	if err == nil {
		panic("Error: expected timeout")
	}

	w.WriteMsg(CreateRespMessageByReq(w.IsTCP(), Valid, req))
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
		Code:      POST,
		MessageID: 1234,
		Payload:   payload,
		Token:     []byte("chall"),
	},
	)
	req0.SetOption(ContentFormat, TextPlain)
	req0.SetPathString(path)

	resp0, err := co.Exchange(req0, 1*time.Second)
	if err != nil {
		t.Fatalf("unable to read msg from server: %v", err)
	}

	res := CreateRespMessageByReq(co.session.IsTCP(), Valid, req0)
	assertEqualMessages(t, res, resp0)
}

func TestServingChallengingClient(t *testing.T) {
	HandleFunc("/challenging", ChallegingServer)
	defer HandleRemove("/challenging")
	testServingTCPwithMsg(t, "udp", make([]byte, 128), simpleChallengingMsg)
}

func TestServingChallengingClientTCP(t *testing.T) {
	HandleFunc("/challenging", ChallegingServer)
	defer HandleRemove("/challenging")
	testServingTCPwithMsg(t, "tcp", make([]byte, 128), simpleChallengingMsg)
}

func TestServingChallengingClientTLS(t *testing.T) {
	HandleFunc("/challenging", ChallegingServer)
	defer HandleRemove("/challenging")
	testServingTCPwithMsg(t, "tcp-tls", make([]byte, 128), simpleChallengingMsg)
}

func TestServingChallengingTimeoutClient(t *testing.T) {
	HandleFunc("/challengingTimeout", ChallegingServerTimeout)
	defer HandleRemove("/challengingTimeout")
	testServingTCPwithMsg(t, "udp", make([]byte, 128), simpleChallengingTimeoutMsg)
}

func TestServingChallengingTimeoutClientTCP(t *testing.T) {
	HandleFunc("/challengingTimeout", ChallegingServerTimeout)
	defer HandleRemove("/challengingTimeout")
	testServingTCPwithMsg(t, "tcp", make([]byte, 128), simpleChallengingTimeoutMsg)
}

func TestServingChallengingTimeoutClientTLS(t *testing.T) {
	HandleFunc("/challengingTimeout", ChallegingServerTimeout)
	defer HandleRemove("/challengingTimeout")
	testServingTCPwithMsg(t, "tcp-tls", make([]byte, 128), simpleChallengingTimeoutMsg)
}

func BenchmarkServe(b *testing.B) {
	b.StopTimer()
	HandleFunc("/test", EchoServer)
	defer HandleRemove("/test")

	s, addrstr, fin, err := RunLocalUDPServer(":0")
	if err != nil {
		b.Fatalf("unable to run test server: %v", err)
	}
	defer func() {
		s.Shutdown()
		<-fin
	}()

	co, err := Dial("udp", addrstr)
	if err != nil {
		b.Fatalf("unable to dialing: %v", err)
	}
	defer co.Close()

	req := &DgramMessage{
		MessageBase{
			typ:       Confirmable,
			code:      POST,
			messageID: 1234,
			payload:   []byte("Content sent by client"),
		}}
	req.SetOption(ContentFormat, TextPlain)
	req.SetPathString("/test")

	b.StartTimer()
	for i := uint32(0); i < uint32(b.N); i++ {
		abc := *req
		token := make([]byte, 8)
		binary.LittleEndian.PutUint32(token, i)
		abc.SetToken(token)
		_, err = co.Exchange(&abc, 5*time.Second)
		if err != nil {
			b.Fatalf("unable to read msg from server: %v", err)
		}
	}
}

func BenchmarkServeTCP(b *testing.B) {
	b.StopTimer()
	HandleFunc("/test", EchoServer)
	defer HandleRemove("/test")

	s, addrstr, fin, err := RunLocalTCPServer(":0")
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

	req := &TcpMessage{
		MessageBase{
			typ:       Confirmable,
			code:      POST,
			messageID: 1234,
			payload:   []byte("Content sent by client"),
		}}
	req.SetOption(ContentFormat, TextPlain)
	req.SetPathString("/test")

	b.StartTimer()
	for i := uint32(0); i < uint32(b.N); i++ {
		abc := *req
		token := make([]byte, 8)
		binary.LittleEndian.PutUint32(token, i)
		abc.SetToken(token)
		_, err = co.Exchange(&abc, 1*time.Second)
		if err != nil {
			b.Fatalf("unable to read msg from server: %v", err)
		}
	}
}

func benchmarkServeTCPStreamWithMsg(b *testing.B, req *TcpMessage) {
	b.StopTimer()
	HandleFunc("/test", EchoServer)
	defer HandleRemove("/test")

	s, addrstr, fin, err := RunLocalTCPServer(":0")
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
	res := CreateRespMessageByReq(true, Valid, req)

	b.StartTimer()
	sync := make(chan bool)

	for i := uint32(0); i < uint32(b.N); i++ {
		go func(t uint32) {
			abc := *req
			token := make([]byte, 8)
			binary.LittleEndian.PutUint32(token, t)
			abc.SetToken(token)
			resp, err := co.Exchange(&abc, 5*time.Second)
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
			typ:       Confirmable,
			code:      POST,
			messageID: 1234,
			payload:   []byte("Content sent by client"),
		},
	}
	req.SetOption(ContentFormat, TextPlain)
	req.SetPathString("/test")
	benchmarkServeTCPStreamWithMsg(b, req)
}

func BenchmarkServeTCPStreamBigMsg(b *testing.B) {
	req := &TcpMessage{
		MessageBase{
			typ:       Confirmable,
			code:      POST,
			messageID: 1234,
			payload:   make([]byte, 1024*1024*10),
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
