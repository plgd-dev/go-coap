
FROM golang:1.27.3-bookworm

# Build pinned Q-Block/OSCORE-enabled libcoap tools and prepare writable directories for UID 1000.
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends build-essential cmake git libssl-dev; \
    source_dir=/tmp/libcoap-source; \
    build_dir=/tmp/libcoap-build; \
    revision=cfa1cf3d1721c1598c40a624b3b0028dd1bbe178; \
    git init "$source_dir"; \
    git -C "$source_dir" remote add origin https://github.com/obgm/libcoap.git; \
    git -C "$source_dir" fetch --depth=1 origin "$revision"; \
    git -C "$source_dir" checkout --detach FETCH_HEAD; \
    test "$(git -C "$source_dir" rev-parse HEAD)" = "$revision"; \
    cmake -S "$source_dir" -B "$build_dir" \
        -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/usr/local \
        -DENABLE_Q_BLOCK=ON \
        -DENABLE_DTLS=ON -DDTLS_BACKEND=openssl \
        -DENABLE_OSCORE=ON -DENABLE_DOCS=OFF -DENABLE_TESTS=OFF \
        -DENABLE_EXAMPLES=ON -DBUILD_SHARED_LIBS=OFF; \
    cmake --build "$build_dir" --parallel 2; \
    cmake --install "$build_dir"; \
    client="$(command -v coap-client-openssl || command -v coap-client-notls || command -v coap-client)"; \
    server="$(command -v coap-server-openssl || command -v coap-server-notls || command -v coap-server)"; \
    if ! command -v coap-client >/dev/null 2>&1; then ln -s "$client" /usr/local/bin/coap-client; fi; \
    if ! command -v coap-client-notls >/dev/null 2>&1; then ln -s "$client" /usr/local/bin/coap-client-notls; fi; \
    if ! command -v coap-server >/dev/null 2>&1; then ln -s "$server" /usr/local/bin/coap-server; fi; \
    if ! command -v coap-server-notls >/dev/null 2>&1; then ln -s "$server" /usr/local/bin/coap-server-notls; fi; \
    command -v coap-client-notls; \
    command -v coap-server-notls; \
    command -v coap-client; \
    command -v coap-server; \
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
ENV LIBCOAP_QBLOCK_REQUIRED=1
ENV LIBCOAP_QBLOCK_MODE=enabled
ENV GOCOAP_REQUIRE_LIBCOAP_OSCORE=1

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

# Q-Block and OSCORE integration tests are mandatory because their required flags prevent skips.
RUN go build ./... && CGO_ENABLED=1 go test -timeout=15m ./...

CMD ["/bin/bash"]
