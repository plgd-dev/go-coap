#!/usr/bin/env bash
set -euo pipefail
repo=$(cd "$(dirname "$0")/../../.." && pwd)
source_dir=${LIBCOAP_SOURCE:-/private/tmp/libcoap-qblock-reference}
build_dir=${LIBCOAP_DTLS_BUILD:-/private/tmp/libcoap-qblock-build/dtls}
openssl_dir=${OPENSSL_ROOT_DIR:-/opt/homebrew/opt/openssl@3}
output_dir=${QBLOCK_INTEROP_OUTPUT:-/private/tmp/qblock-interop-dtls-$(date +%Y%m%d-%H%M%S)}
pin=c63c8f7cb7f248a4992539529b9e1b691962f29a
if [[ "$(rtk proxy git -C "$source_dir" rev-parse HEAD)" != "$pin" ]] || [[ -n "$(rtk proxy git -C "$source_dir" status --porcelain --untracked-files=all)" ]]; then
  echo "Expected clean pinned libcoap source $pin" >&2
  exit 1
fi
rtk proxy mkdir -p "$build_dir"
rtk proxy cmake -S "$source_dir" -B "$build_dir" \
  -DENABLE_Q_BLOCK=ON -DENABLE_EXAMPLES=ON -DENABLE_TESTS=OFF \
  -DENABLE_DOCS=OFF -DENABLE_DTLS=ON -DDTLS_BACKEND=openssl \
  -DOPENSSL_ROOT_DIR="$openssl_dir" -DENABLE_TCP=OFF -DENABLE_WS=OFF \
  -DENABLE_OSCORE=OFF -DBUILD_SHARED_LIBS=OFF -DCMAKE_BUILD_TYPE=Debug \
  > "$build_dir/configure.log" 2>&1
rtk proxy cmake --build "$build_dir" --parallel 4 > "$build_dir/build.log" 2>&1
cd "$repo"
export GOCACHE="${GOCACHE:-/private/tmp/go-coap-con-build}"
provenance_dir="$output_dir-source"
rtk proxy python3 tests/interop/qblock/provenance.py capture --output "$provenance_dir"
rtk proxy cc -std=gnu11 -g -I"$build_dir" -I"$build_dir/include" -I"$source_dir/include" \
  "$repo/tests/interop/qblock/libcoap-server.c" "$build_dir/libcoap-3.a" \
  -L"$openssl_dir/lib" -lssl -lcrypto -o "$build_dir/libcoap-fixture-server"
cd "$repo"
rtk proxy env GOCACHE="${GOCACHE:-/private/tmp/go-coap-con-build}" go build -o "$build_dir/go-fixture" ./tests/interop/qblock/fixture
rtk proxy python3 tests/interop/qblock/provenance.py verify --output "$provenance_dir"
echo "DTLS evidence: $output_dir"
rtk proxy python3 tests/interop/qblock/harness.py --build "$build_dir" --fixture "$build_dir/go-fixture" \
  --output "$output_dir" --server-fixture --transport dtls --provenance "$provenance_dir"
