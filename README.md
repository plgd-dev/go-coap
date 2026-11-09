# Go-CoAP

[![Build Status](https://github.com/plgd-dev/go-coap/workflows/Test/badge.svg)](https://github.com/plgd-dev/go-coap/actions?query=workflow%3ATest)
[![Quality Gate Status](https://sonarcloud.io/api/project_badges/measure?project=plgd-dev_go-coap&metric=alert_status)](https://sonarcloud.io/summary/new_code?id=plgd-dev_go-coap)
[![Coverage](https://img.shields.io/sonar/coverage/plgd-dev_go-coap?server=https%3A%2F%2Fsonarcloud.io)](https://sonarcloud.io/summary/overall?id=plgd-dev_go-coap)
[![FOSSA Status](https://app.fossa.io/api/projects/git%2Bgithub.com%2Fplgd-dev%2Fgo-coap.svg?type=shield)](https://app.fossa.io/projects/git%2Bgithub.com%2Fplgd-dev%2Fgo-coap?ref=badge_shield)
[![contributors](https://img.shields.io/github/contributors/plgd-dev/go-coap)](https://github.com/plgd-dev/go-coap/graphs/contributors)
[![GitHub stars](https://img.shields.io/github/stars/plgd-dev/go-coap)](https://github.com/plgd-dev/go-coap/stargazers)
[![GitHub license](https://img.shields.io/github/license/plgd-dev/go-coap)](https://github.com/plgd-dev/go-coap/blob/master/LICENSE)
[![GoDoc](https://pkg.go.dev/badge/github.com/plgd-dev/go-coap/v3?utm_source=godoc)](https://pkg.go.dev/github.com/plgd-dev/go-coap/v3?utm_source=godoc)
<!-- [![Go Report](https://goreportcard.com/badge/github.com/plgd-dev/go-coap)](https://goreportcard.com/report/github.com/plgd-dev/go-coap) -->

The Constrained Application Protocol (CoAP) is a specialized web transfer protocol for use with constrained nodes and constrained networks in the Internet of Things.
The protocol is designed for machine-to-machine (M2M) applications such as smart energy and building automation.

The go-coap provides servers and clients for DTLS, TCP-TLS, UDP, TCP in golang language.

## Features

* CoAP over UDP [RFC 7252][coap].
* Observe resources in CoAP [RFC 7641][coap-observe]
* Block-wise transfers in CoAP [RFC 7959][coap-block-wise-transfers]
* CoAP NoResponse option in CoAP [RFC 7967][coap-noresponse]
* FETCH, PATCH, and iPATCH methods [RFC 8132][coap-fetch-patch]
* CoAP over TCP/TLS [RFC 8323][coap-tcp]
* Too many requests response code [RFC 8516][coap-429]
* CoAP Echo and Request-Tag options [RFC 9175][coap-echo-request-tag]
* Experimental Q-Block datagram transfers [RFC 9177][coap-q-block-wise-transfers]
* CoAP over DTLS [pion/dtls][pion-dtls]
* Object Security for Constrained RESTful Environments (OSCORE) [RFC 8613][oscore]
* request multiplexer
* multicast

[coap]: http://tools.ietf.org/html/rfc7252
[coap-observe]: https://tools.ietf.org/html/rfc7641
[coap-block-wise-transfers]: https://tools.ietf.org/html/rfc7959
[coap-noresponse]: https://tools.ietf.org/html/rfc7967
[coap-fetch-patch]: https://www.rfc-editor.org/rfc/rfc8132.html
[coap-tcp]: https://tools.ietf.org/html/rfc8323
[coap-429]: https://datatracker.ietf.org/doc/html/rfc8516
[coap-echo-request-tag]: https://www.rfc-editor.org/rfc/rfc9175.html
[coap-q-block-wise-transfers]: https://www.rfc-editor.org/rfc/rfc9177.html
[pion-dtls]: https://github.com/pion/dtls
[oscore]: https://www.rfc-editor.org/rfc/rfc8613.html

## Requirements

* Go 1.26 or higher

## Experimental Q-Block transfers

Q-Block is opt-in for UDP/DTLS; existing blockwise defaults are unchanged.
See the [QBlockwise guide](docs/qblockwise/qblockwise.md) for configuration, limits,
libcoap interoperability tests for Blockwise and Q-Block transfers (including the
RFC 8132 methods), and a runnable
[client](examples/qblockwise/client/main.go) /
[server](examples/qblockwise/server/main.go) example.

## OSCORE

OSCORE protects CoAP messages using application-provisioned security contexts.
See the [OSCORE guide](docs/oscore/oscore.md) for configuration, sequence persistence,
supported transports, and limitations. A runnable UDP
[client/server example](examples/oscore/README.md) demonstrates protected
requests and durable sender/replay state.

## Samples

### FETCH, PATCH, and iPATCH (RFC 8132)

The [simple client](examples/simple/client/main.go) and
[server](examples/simple/server/main.go) demonstrate all three methods on
`/object`: FETCH selects a JSON field, PATCH appends with JSON Patch, and iPATCH
replaces the value with JSON Merge Patch. The
[Q-Blockwise client](examples/qblockwise/client/main.go) and
[server](examples/qblockwise/server/main.go) demonstrate the methods with large
payloads; their application-defined octet-stream behavior uses FETCH to read,
PATCH to append, and iPATCH to replace data.

Run either pair in separate terminals:

```sh
go run ./examples/simple/server
go run ./examples/simple/client
```

```sh
go run ./examples/qblockwise/server
go run ./examples/qblockwise/client
```

### Simple

#### Server UDP/TCP

```go
    // Server

    // Middleware function, which will be called for each request.
    func loggingMiddleware(next mux.Handler) mux.Handler {
        return mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
            log.Printf("ClientAddress %v, %v\n", w.Conn().RemoteAddr(), r.String())
            next.ServeCOAP(w, r)
        })
    }

    // See /examples/simple/server/main.go
    func handleA(w mux.ResponseWriter, req *mux.Message) {
        err := w.SetResponse(codes.GET, message.TextPlain, bytes.NewReader([]byte("hello world")))
        if err != nil {
            log.Printf("cannot set response: %v", err)
        }
    }

    func main() {
        r := mux.NewRouter()
        r.Use(loggingMiddleware)
        r.Handle("/a", mux.HandlerFunc(handleA))
        r.Handle("/b", mux.HandlerFunc(handleB))

        log.Fatal(coap.ListenAndServe("udp", ":5688", r))


        // for tcp
        // log.Fatal(coap.ListenAndServe("tcp", ":5688",  r))

        // for tcp-tls
        // log.Fatal(coap.ListenAndServeTLS("tcp", ":5688", &tls.Config{...}, r))

        // for udp-dtls (deprecated: *dtls.Config is deprecated in pion/dtls v3, prefer options-based API)
        // log.Fatal(coap.ListenAndServeDTLS("udp", ":5688", &dtls.Config{...}, r))

        // for udp-dtls (options-based API — pion/dtls v3)
        // log.Fatal(coap.ListenAndServeDTLS("udp", ":5688",
        //     net.NewDTLSServerOptions(dtls.WithPSK(...), dtls.WithCertificates(...)), r))
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

        // for dtls (deprecated: *dtls.Config is deprecated in pion/dtls v3, prefer options-based API)
        // co, err := dtls.Dial("localhost:5688", &dtls.Config{...})

        // for dtls (options-based API — pion/dtls v3)
        // co, err := dtls.Dial("localhost:5688",
        //     dtlscoap.NewDTLSClientOptions(piondtls.WithPSK(...), piondtls.WithCertificates(...)))

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

### DTLS Options-based API (pion/dtls v3)

> **Deprecation notice:** `*dtls.Config` is [deprecated in pion/dtls v3](https://github.com/pion/dtls) in favour of immutable options-based configurations. Its use in go-coap is consequently deprecated as well and may be removed in a future major version. Migrate to `net.NewDTLSServerOptions` / `dtls.NewDTLSClientOptions` as shown below.

In addition to the legacy `*dtls.Config`, go-coap supports the **options-based API** introduced in pion/dtls v3. Both approaches are currently accepted by the same generic functions (`ListenAndServeDTLS`, `Dial`, `NewDTLSListener`).

Use `net.NewDTLSServerOptions(...)` / `dtls.NewDTLSClientOptions(...)` to compose DTLS configurations from individual options:

```go
import (
    coap   "github.com/plgd-dev/go-coap/v3"
    coapnet "github.com/plgd-dev/go-coap/v3/net"
    coapdtls "github.com/plgd-dev/go-coap/v3/dtls"
    piondtls "github.com/pion/dtls/v3"
)

// Server
serverOpts := coapnet.NewDTLSServerOptions(
    piondtls.WithPSK(func(hint []byte) ([]byte, error) {
        return []byte{0xAB, 0xC1, 0x23}, nil
    }),
    piondtls.WithCipherSuites(piondtls.TLS_PSK_WITH_AES_128_CCM_8),
)
log.Fatal(coap.ListenAndServeDTLS("udp", ":5688", serverOpts, r))

// Client
clientOpts := coapdtls.NewDTLSClientOptions(
    piondtls.WithPSK(func(hint []byte) ([]byte, error) {
        return []byte{0xAB, 0xC1, 0x23}, nil
    }),
    piondtls.WithCipherSuites(piondtls.TLS_PSK_WITH_AES_128_CCM_8),
)
co, err := coapdtls.Dial("localhost:5688", clientOpts)
```

See [examples/options/server/](examples/options/server/) and [examples/options/client/](examples/options/client/) for complete working examples using the options-based API.

### Observe / Notify

[Server](examples/observe/server/main.go) example.

[Client](examples/observe/client/main.go) example.

### Multicast

[Server](examples/mcast/server/main.go) example.

[Client](examples/mcast/client/main.go) example.

## License

Apache 2.0

[![FOSSA Status](https://app.fossa.io/api/projects/git%2Bgithub.com%2Fplgd-dev%2Fgo-coap.svg?type=large)](https://app.fossa.io/projects/git%2Bgithub.com%2Fplgd-dev%2Fgo-coap?ref=badge_large)

## Support go-coap

If go-coap is useful to you, you can buy us a coffee for $3 or choose another amount.

* **By card:** [Sponsor plgd-dev on GitHub](https://github.com/sponsors/plgd-dev) and choose a one-time $3 sponsorship or enter a custom amount. This requires GitHub Sponsors to be enabled for the `plgd-dev` organization.
