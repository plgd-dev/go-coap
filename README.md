[![Build Status](https://travis-ci.com/ondrejtomcik/go-coap.svg?branch=master)](https://travis-ci.com/ondrejtomcik/go-coap)
[![codecov](https://codecov.io/gh/ondrejtomcik/go-coap/branch/master/graph/badge.svg)](https://codecov.io/gh/ondrejtomcik/go-coap)
[![Go Report](https://goreportcard.com/badge/github.com/ondrejtomcik/go-coap)](https://goreportcard.com/report/github.com/ondrejtomcik/go-coap)

# CoAP Client and Server for go

Features supported:
* CoAP over UDP [RFC 7252][coap].
* CoAP over TCP/TLS [RFC 8232][coap-tcp]
* request multiplexer

Not yet implemented:
* CoAP over DTLS

Fork of https://github.com/dustin/go-coap

[coap]: http://tools.ietf.org/html/rfc7252
[coap-tcp]: https://tools.ietf.org/html/rfc8323

## Samples

### Simple

#### Server UDP/TCP
```go
	// Server
	// See /examples/simpler/server/main.go
	func handleA(w coap.ResponseWriter, req coap.Message) {
		log.Printf("Got message in handleA: path=%q: %#v from %v", req.Path(), req, w.RemoteAddr())
		if req.IsConfirmable() {
			res := w.NewMessage(coap.MessageParams{
				Type:      coap.Acknowledgement,
				Code:      coap.Content,
				MessageID: req.MessageID(),
				Token:     req.Token(),
				Payload:   []byte("server: hello world!"),
			})
			res.SetOption(coap.ContentFormat, coap.TextPlain)

			log.Printf("Transmitting from B %#v", res)
			w.WriteMsg(res)
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
		defer co.Close()

		req := co.NewMessage(coap.MessageParams{
			Type:      coap.Confirmable,
			Code:      coap.GET,
			MessageID: 12345,
			Payload:   []byte("client: hello, world!"),
		})
		req.SetPathString("/a")

		rv, _, err := co.Exchange(req)
		if err != nil {
			log.Fatalf("Error sending request: %v", err)
		}

		if rv != nil {
			log.Printf("Response payload: %v", rv.Payload())
		}
	}
```


### Observe / Notify

#### Server
```go
	// Server
	// See /examples/observe/server/main.go
	func periodicTransmitter(w coap.ResponseWriter, req coap.Message) {
		subded := time.Now()

		for {
			msg := w.NewMessage(coap.MessageParams{
				Type:      coap.Acknowledgement,
				Code:      coap.Content,
				MessageID: req.MessageID(),
				Payload:   []byte(fmt.Sprintf("Been running for %v", time.Since(subded))),
			})

			msg.SetOption(coap.ContentFormat, coap.TextPlain)
			msg.SetOption(coap.LocationPath, req.Path())

			log.Printf("Transmitting %v", msg)
			err := w.WriteMsg(msg)
			if err != nil {
				log.Printf("Error on transmitter, stopping: %v", err)
				return
			}

			time.Sleep(time.Second)
		}
	}

	func main() {
		log.Fatal(coap.ListenAndServe(":5688", "udp",
			coap.HandlerFunc(func(w coap.ResponseWriter, req coap.Message) {
				log.Printf("Got message path=%q: %#v from %v", req.Path(), req, w.RemoteAddr())
				if req.Code() == coap.GET && req.Option(coap.Observe) != nil {
					value := req.Option(coap.Observe)
					if value.(uint32) > 0 {
						go periodicTransmitter(w, req)
					} else {
						log.Printf("coap.Observe value=%v", value)
					}
				}
			})))
}
```

#### Client
```go
	// Client
	// See /examples/observe/client/main.go
	func main() {
		conn, err := coap.Dial("udp", "localhost:5688")
		if err != nil {
			log.Fatalf("Error dialing: %v", err)
		}
		defer conn.Close()

		req := conn.NewMessage(coap.MessageParams{
			Type:      coap.NonConfirmable,
			Code:      coap.GET,
			MessageID: 12345,
		})

		req.AddOption(coap.Observe, 1)
		req.SetPathString("/some/path")

		err = conn.WriteMsg(req, 1*time.Second)
		if err != nil {
			log.Fatalf("Error sending request: %v", err)
		}

		for err == nil {
			var rv coap.Message
			rv, err = conn.ReadMsg(2 * time.Second)
			if err != nil {
				log.Fatalf("Error receiving: %v", err)
			} else if rv != nil {
				log.Printf("Got %s", rv.Payload())
			}

		}
		log.Printf("Done...\n")

	}
```

## License
MIT
