
FROM golang:1.27.3-bookworm

# Install libcoap tools and prepare writable directories for UID 1000.
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends libcoap3-bin; \
    ln -sf /usr/bin/coap-client-notls /usr/bin/coap-client; \
    command -v coap-client; \
    command -v coap-server || command -v coap-server-notls; \
    install -d -o 1000 -g 1000 \
        /src \
        /home/1000/.cache/go-build \
        /home/1000/go/pkg/mod; \
    rm -rf /var/lib/apt/lists/*

# Configure writable Go directories.
ENV HOME=/home/1000
ENV GOPATH=/home/1000/go
ENV GOCACHE=/home/1000/.cache/go-build
ENV GOMODCACHE=/home/1000/go/pkg/mod
ENV GOTOOLCHAIN=local

WORKDIR /src

# Download dependencies while network is available.
COPY --chown=1000:1000 go.mod go.sum ./

USER 1000:1000

RUN go mod download

# Copy source and Git metadata with correct ownership.
COPY --chown=1000:1000 . .

# Disable network-based dependency resolution.
ENV GOPROXY=off
ENV GOSUMDB=off

# Verify build and tests offline.
RUN go build ./... && go test ./...

CMD ["/bin/bash"]
