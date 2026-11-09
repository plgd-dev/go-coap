# OSCORE client/server example

This example runs a UDP CoAP server and client with pre-established OSCORE
contexts. Both programs require the same master secret. Set it in the
environment rather than putting it on the command line:

```sh
export GOCOAP_OSCORE_MASTER_SECRET="$(openssl rand -hex 16)"
```

In one terminal, start the server:

```sh
go run ./examples/oscore/server
```

In another terminal, with the same `GOCOAP_OSCORE_MASTER_SECRET`, send a
protected GET request:

```sh
go run ./examples/oscore/client
```

The client logs `Hello over OSCORE`. Use `-addr` to change the listen or server
address, `-path` to select another resource, and `-state-dir` to select where
the sender sequence and receiver replay state are stored. The default state
directory is `./oscore-state`; the client and server use distinct stable store
IDs, so they can share a directory on one host. Preserve each peer's state
directory when restarting it with the same OSCORE identity. Never reset sender
sequence state or use the example secret and IDs in production.
