package coap

import (
	"bytes"
	"crypto/tls"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"
)

const gomaxprocs = 1

func CreateRespMessageByReq(isTCP bool, code COAPCode, req Message) Message {
	if isTCP {
		resp := &TcpMessage{
			MessageBase{
				//typ:       Acknowledgement, elided by COAP over TCP
				code: Valid,
				//messageID: req.MessageID(), , elided by COAP over TCP
				payload: req.Payload(),
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
		},
	}
	resp.SetPath(req.Path())
	resp.SetOption(ContentFormat, req.Option(ContentFormat))
	return resp
}

func EchoServer(w ResponseWriter, req Message) {
	if req.IsConfirmable() {
		w.WriteMsg(CreateRespMessageByReq(w.IsTCP(), Valid, req))
	}
}

func EchoServerBadID(w ResponseWriter, req Message) {
	if req.IsConfirmable() {
		w.WriteMsg(CreateRespMessageByReq(w.IsTCP(), BadRequest, req))
	}
}

func RunLocalUDPServer(laddr string) (*Server, string, error) {
	server, l, _, err := RunLocalUDPServerWithFinChan(laddr)

	return server, l, err
}

func RunLocalUDPServerWithFinChan(laddr string) (*Server, string, chan error, error) {
	pc, err := net.ListenPacket("udp", laddr)
	if err != nil {
		return nil, "", nil, err
	}
	server := &Server{PacketConn: pc, ReadTimeout: time.Hour, WriteTimeout: time.Hour}

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

func RunLocalTCPServer(laddr string) (*Server, string, error) {
	server, l, _, err := RunLocalTCPServerWithFinChan(laddr)

	return server, l, err
}

func RunLocalTCPServerWithFinChan(laddr string) (*Server, string, chan error, error) {
	l, err := net.Listen("tcp", laddr)
	if err != nil {
		return nil, "", nil, err
	}

	server := &Server{Listener: l, ReadTimeout: time.Hour, WriteTimeout: time.Hour}

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

func RunLocalTLSServer(laddr string, config *tls.Config) (*Server, string, error) {
	l, err := tls.Listen("tcp", laddr, config)
	if err != nil {
		return nil, "", err
	}

	server := &Server{Listener: l, ReadTimeout: time.Hour, WriteTimeout: time.Hour}

	waitLock := sync.Mutex{}
	waitLock.Lock()
	server.NotifyStartedFunc = waitLock.Unlock

	go func() {
		server.ActivateAndServe()
		l.Close()
	}()

	waitLock.Lock()
	return server, l.Addr().String(), nil
}

func testServingTCPwithMsg(t *testing.T, net string, msgSize int) {
	HandleFunc("/test", EchoServer)
	defer HandleRemove("/test")

	var s *Server
	var addrstr string
	var err error
	c := new(Client)
	c.Net = net
	isTCP := false
	payload := make([]byte, msgSize)
	switch net {
	case "tcp", "tcp4", "tcp6":
		isTCP = true
		s, addrstr, err = RunLocalTCPServer(":0")
	case "udp", "udp4", "udp6":
		s, addrstr, err = RunLocalUDPServer(":0")
	case "tcp-tls", "tcp4-tls", "tcp6-tls":
		isTCP = true
		cert, err := tls.X509KeyPair(CertPEMBlock, KeyPEMBlock)
		if err != nil {
			t.Fatalf("unable to build certificate: %v", err)
		}
		config := tls.Config{
			Certificates: []tls.Certificate{cert},
		}
		s, addrstr, err = RunLocalTLSServer(":0", &config)
		if err != nil {
			t.Fatalf("unable to run test server: %v", err)
		}
		c.TLSConfig = &tls.Config{InsecureSkipVerify: true}
	}

	if err != nil {
		t.Fatalf("unable to run test server: %v", err)
	}
	defer s.Shutdown()

	co, err := c.Dial(addrstr)
	if err != nil {
		t.Fatalf("unable to dialing: %v", err)
	}
	defer co.Close()

	req := co.NewMessage(MessageParams{
		Type:      Confirmable,
		Code:      POST,
		MessageID: 1234,
		Payload:   payload,
	},
	)
	req.SetOption(ContentFormat, TextPlain)
	req.SetPathString("/test")

	res := CreateRespMessageByReq(isTCP, Valid, req)

	m, _, err := co.Exchange(req)
	if err != nil {
		t.Fatal("failed to exchange", err)
	}
	if m == nil {
		t.Fatalf("Didn't receive CoAP response")
	}
	assertEqualMessages(t, res, m)
}

func TestServingUDP(t *testing.T) {
	testServingTCPwithMsg(t, "udp", 128)
}

func TestServingUDPBigMsg(t *testing.T) {
	testServingTCPwithMsg(t, "udp", 1300)
}

func TestServingTCP(t *testing.T) {
	testServingTCPwithMsg(t, "tcp", 128)
}

func TestServingTCPBigMsg(t *testing.T) {
	testServingTCPwithMsg(t, "tcp", 10*1024*1024)
}

func TestServingTLS(t *testing.T) {
	testServingTCPwithMsg(t, "tcp-tls", 128)
}

func TestServingTLSBigMsg(t *testing.T) {
	testServingTCPwithMsg(t, "tcp-tls", 10*1024*1024)
}

func BenchmarkServe(b *testing.B) {
	b.StopTimer()
	HandleFunc("/test", EchoServer)
	defer HandleRemove("/test")
	a := runtime.GOMAXPROCS(gomaxprocs)

	s, addrstr, err := RunLocalUDPServer(":0")
	if err != nil {
		b.Fatalf("unable to run test server: %v", err)
	}
	defer s.Shutdown()

	co, err := Dial("udp", addrstr)
	if err != nil {
		b.Fatalf("unable to dialing: %v", err)
	}
	defer co.Close()

	req := co.NewMessage(
		MessageParams{
			Type:      Confirmable,
			Code:      POST,
			MessageID: 1234,
			Payload:   []byte("Content sent by client"),
		})
	req.SetOption(ContentFormat, TextPlain)
	req.SetPathString("/test")

	b.StartTimer()
	for i := 0; i < b.N; i++ {
		co.Exchange(req)
	}
	runtime.GOMAXPROCS(a)
}

func BenchmarkServeTCP(b *testing.B) {
	b.StopTimer()
	HandleFunc("/test", EchoServer)
	defer HandleRemove("/test")
	a := runtime.GOMAXPROCS(gomaxprocs)

	s, addrstr, err := RunLocalTCPServer(":0")
	if err != nil {
		b.Fatalf("unable to run test server: %v", err)
	}
	defer s.Shutdown()

	co, err := Dial("tcp", addrstr)
	if err != nil {
		b.Fatalf("unable dialing: %v", err)
	}
	defer co.Close()

	req := co.NewMessage(
		MessageParams{
			Type:      Confirmable,
			Code:      POST,
			MessageID: 1234,
			Payload:   []byte("Content sent by client"),
		})
	req.SetOption(ContentFormat, TextPlain)
	req.SetPathString("/test")

	b.StartTimer()
	for i := 0; i < b.N; i++ {
		co.Exchange(req)
	}
	runtime.GOMAXPROCS(a)
}

func benchmarkServeTCPStreamWithMsg(b *testing.B, req *TcpMessage) {
	b.StopTimer()
	HandleFunc("/test", EchoServer)
	defer HandleRemove("/test")
	a := runtime.GOMAXPROCS(gomaxprocs)

	s, addrstr, err := RunLocalTCPServer(":0")
	if err != nil {
		b.Fatalf("unable to run test server: %v", err)
	}
	defer s.Shutdown()

	co, err := Dial("tcp", addrstr)
	if err != nil {
		b.Fatalf("unable dialing: %v", err)
	}
	defer co.Close()
	res := CreateRespMessageByReq(true, Valid, req)

	b.StartTimer()
	for i := 0; i < b.N; i++ {
		if err = co.WriteMsg(req, coapTimeout); err != nil {
			b.Fatalf("write to run test client: %v", err)
		}
	}
	for i := 0; i < b.N; i++ {
		m, err := co.ReadMsg(coapTimeout)
		if err != nil {
			b.Fatalf("unable to read msg from server: %v", err)
		}
		b.StopTimer()
		if !bytes.Equal(res.Payload(), m.Payload()) {
			b.Fatalf("unable to read msg from server: %v", err)
		}
		b.StartTimer()
	}
	runtime.GOMAXPROCS(a)
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
