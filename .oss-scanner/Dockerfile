
FROM golang:1.27.3-bookworm

# Install libcoap tools required by integration tests.
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends libcoap3-bin; \
    ln -sf /usr/bin/coap-client-notls /usr/bin/coap-client; \
    command -v coap-client; \
    coap-client -h 2>&1 | head -n 1 || true; \
    test -x "$(command -v coap-client)"; \
    command -v coap-server || command -v coap-server-notls; \
    rm -rf /var/lib/apt/lists/*

WORKDIR /src

# Download Go dependencies before offline execution.
COPY go.mod go.sum ./
RUN go mod download

# Copy the complete repository.
COPY . .

# Disable network-based dependency and toolchain resolution.
ENV GOPROXY=off
ENV GOSUMDB=off
ENV GOTOOLCHAIN=local

# Verify build and tests.
RUN go build ./...
RUN go test ./...

CMD ["/bin/bash"]
