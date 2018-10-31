[![Build Status](https://travis-ci.com/go-ocf/go-coap.svg?branch=master)](https://travis-ci.com/go-ocf/go-coap)
[![codecov](https://codecov.io/gh/go-ocf/go-coap/branch/master/graph/badge.svg)](https://codecov.io/gh/go-ocf/go-coap)
[![Go Report](https://goreportcard.com/badge/github.com/go-ocf/go-coap)](https://goreportcard.com/report/github.com/go-ocf/go-coap)

# CoAP Client and Server for go

Features supported:
* CoAP over UDP [RFC 7252][coap].
* CoAP over TCP/TLS [RFC 8232][coap-tcp]
* Observe resources in CoAP [RFC 7641][coap-observe]
* Block-wise transfers in COAP [RFC 7959][coap-block-wise-transfers]
* request multiplexer
* multicast

Not yet implemented:
* CoAP over DTLS

[coap]: http://tools.ietf.org/html/rfc7252
[coap-tcp]: https://tools.ietf.org/html/rfc8323
[coap-block-wise-transfers]: https://tools.ietf.org/html/rfc7959
[coap-observe]: https://tools.ietf.org/html/rfc7641

## Samples

### Simple

#### Server UDP/TCP
```go
	// Server
	// See /examples/simple/server/main.go
	func handleA(w coap.ResponseWriter, req *coap.Request) {
		log.Printf("Got message in handleA: path=%q: %#v from %v", req.Msg.Path(), req.Msg, req.Client.RemoteAddr())
		w.SetContentFormat(coap.TextPlain)
		log.Printf("Transmitting from A")
		if _, err := w.Write([]byte("hello world")); err != nil {
			log.Printf("Cannot send response: %v", err)
		}
	}

	func main() {
		mux := coap.NewServeMux()
		mux.Handle("/a", coap.HandlerFunc(handleA))

		log.Fatal(coap.ListenAndServe(":5688", "udp", mux))
		
		// for tcp
		// log.Fatal(coap.ListenAndServe(":5688", "tcp", mux))

		// fot tcp-tls
		// log.Fatal(coap.ListenAndServeTLS(":5688", CertPEMBlock, KeyPEMBlock, mux))
	}
```
#### Client
```go
	// Client
	// See /examples/simpler/client/main.go
	func main() {
		co, err := coap.Dial("udp", "localhost:5688")
		
		// for tcp
		// co, err := coap.Dial("tcp", "localhost:5688")
		
		// for tcp-tls
		// co, err := coap.DialWithTLS("localhost:5688", &tls.Config{InsecureSkipVerify: true})

		if err != nil {
			log.Fatalf("Error dialing: %v", err)
		}

		resp, err := co.Get(path)

		if err != nil {
			log.Fatalf("Error sending request: %v", err)
		}

		log.Printf("Response payload: %v", resp.Payload())
	}
```


### Observe / Notify

#### Server
Look to examples/observe/server/main.go

#### Client
Look to examples/observe/client/main.go


### Multicast

#### Server
Look to examples/mcast/server/main.go

#### Client
Look to examples/mcast/client/main.go

## License
MIT
