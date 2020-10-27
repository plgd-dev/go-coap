# Server And Client PKI generation
The certificates used in the pki examples are generated using golang crypto library.
You can also generate them using openssl, as seen below.

## Generate self signed CA
```sh
CERT_SUBJ="/C=BR/ST=Parana/L=Curitiba/O=Dis/CN=example.com"
openssl ecparam -name secp224r1 -genkey -noout -out root_ca_key.pem
openssl ec -in root_ca_key.pem -pubout -out root_ca_pubkey.pem
openssl req -new -key root_ca_key.pem -x509 -nodes -days 365 -out root_ca_cert.pem -subj $CERT_SUBJ
```

## Generate server
```sh
openssl ecparam -name secp224r1 -genkey -noout -out server_key.pem
openssl req -new -sha256 -key server_key.pem -subj $CERT_SUBJ -out server.csr
openssl x509 -req -in server.csr  -CA root_ca_cert.pem -CAkey root_ca_key.pem -CAcreateserial -out server_cert.pem -days 500 -sha256
```

## Generate client
```sh
CERT_SUBJ="/C=BR/ST=Parana/L=Curitiba/O=Dis/CN=example.com/emailAddress=client1@example.com"
openssl ecparam -name secp224r1 -genkey -noout -out client_key.pem
openssl req -new -sha256 -key client_key.pem -subj $CERT_SUBJ -out client.csr
openssl x509 -req -in client.csr  -CA root_ca_cert.pem -CAkey root_ca_key.pem -CAcreateserial -out client_cert.pem -days 500 -sha256
```
