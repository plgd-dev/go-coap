[![Build Status](https://travis-ci.com/go-ocf/go-coap.svg?branch=master)](https://travis-ci.com/go-ocf/go-coap)
[![codecov](https://codecov.io/gh/go-ocf/go-coap/branch/master/graph/badge.svg)](https://codecov.io/gh/go-ocf/go-coap)
[![Go Report](https://goreportcard.com/badge/github.com/go-ocf/go-coap)](https://goreportcard.com/report/github.com/go-ocf/go-coap)
[![FOSSA Status](https://app.fossa.io/api/projects/git%2Bgithub.com%2Fgo-ocf%2Fgo-coap.svg?type=shield)](https://app.fossa.io/projects/git%2Bgithub.com%2Fgo-ocf%2Fgo-coap?ref=badge_shield)

# CoAP Client and Server for go

Features supported:
* CoAP over UDP [RFC 7252][coap].
* CoAP over TCP/TLS [RFC 8232][coap-tcp]
* Observe resources in CoAP [RFC 7641][coap-observe]
* Block-wise transfers in CoAP [RFC 7959][coap-block-wise-transfers]
* request multiplexer
* multicast
* CoAP NoResponse option in CoAP [RFC 7967][coap-noresponse]
* CoAP over DTLS [pion/dtls][pion-dtls]

[coap]: http://tools.ietf.org/html/rfc7252
[coap-tcp]: https://tools.ietf.org/html/rfc8323
[coap-block-wise-transfers]: https://tools.ietf.org/html/rfc7959
[coap-observe]: https://tools.ietf.org/html/rfc7641
[coap-noresponse]: https://tools.ietf.org/html/rfc7967
[pion-dtls]: https://github.com/pion/dtls

## Samples

### Simple

#### Server UDP/TCP
```go
	// Server
	// See /examples/simple/server/main.go
	func handleA(w mux.ResponseWriter, req *message.Message) {
		log.Printf("got message in handleA:  %+v from %v\n", req, w.ClientConn().RemoteAddr())
		err := w.SetResponse(codes.GET, message.TextPlain, bytes.NewReader([]byte("hello world")))
		if err != nil {
			log.Printf("cannot set response: %v", err)
		}
	}

	func main() {
		m := mux.NewServeMux()
		m.Handle("/a", mux.HandlerFunc(handleA))
		m.Handle("/b", mux.HandlerFunc(handleB))

		log.Fatal(coap.ListenAndServe("udp", ":5688", m))

		
		// for tcp
		// log.Fatal(coap.ListenAndServe("tcp", ":5688",  m))

		// for tcp-tls
		// log.Fatal(coap.ListenAndServeTLS("tcp-tls", ":5688", &tls.Config{...}, m))

		// for udp-dtls
		// log.Fatal(coap.ListenAndServeDTLS("udp-dtls", ":5688", &dtls.Config{...}, m))
	}
```
#### Client
```go
	// Client
	// See /examples/simpler/client/main.go
	func main() {
		co, err := udp.Dial("localhost:5688")
		
		// for tcp
		// co, err := tcp.Dial("localhost:5688")
		
		// for tcp-tls
		// co, err := tcp.Dial("localhost:5688", tcp.WithTLS(&tls.Config{...}))

		// for dtls
		// co, err := dtls.Dial("localhost:5688", &dtls.Config{...}))

		if err != nil {
			log.Fatalf("Error dialing: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		resp, err := co.Get(ctx, "/a")
		if err != nil {
			log.Fatalf("Cannot get response: %v", err)
			return
		}
		log.Printf("Response: %+v", resp)
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

## Contributing

In order to run the tests that the CI will run locally, the following two commands can be used to build the Docker image and run the tests. When making changes, these are the tests that the CI will run, so please make sure that the tests work locally before committing.

```shell
$ docker build . --network=host -t go-coap:build --target build
$ docker run --mount type=bind,source="$(pwd)",target=/shared,readonly --network=host go-coap:build go test './...'
```

## License
Apache 2.0

[![FOSSA Status](https://app.fossa.io/api/projects/git%2Bgithub.com%2Fgo-ocf%2Fgo-coap.svg?type=large)](https://app.fossa.io/projects/git%2Bgithub.com%2Fgo-ocf%2Fgo-coap?ref=badge_large)
