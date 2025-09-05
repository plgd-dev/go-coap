# DTLS Examples

This directory contains examples of how to use go-coap with DTLS.

## PKI

The `pki` example demonstrates how to use DTLS with Public Key Infrastructure (PKI). The client and server generate their own certificates and use them to authenticate each other.

To run the example:

```bash
# run the server
go run examples/dtls/pki/server/main.go
```

```bash
# run the client
go run examples/dtls/pki/client/main.go
```

## PSK

The `psk` example demonstrates how to use DTLS with a Pre-Shared Key (PSK). The client and server use a pre-shared key to authenticate each other.

To run the example:

```bash
# run the server
go run examples/dtls/psk/server/main.go
```

```bash
# run the client
go run examples/dtls/psk/client/main.go
```

## CID

The `cid` example demonstrates how to use DTLS with Connection ID (CID). The client and server use a connection ID to identify the connection, which allows the connection to be resumed on a different address.

To run the example:

```bash
# run the server
go run examples/dtls/cid/server/main.go
```

```bash
# run the client
go run examples/dtls/cid/client/main.go
```
