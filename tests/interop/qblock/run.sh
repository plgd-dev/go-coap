#!/usr/bin/env bash
set -euo pipefail

repo=$(cd "$(dirname "$0")/../../.." && pwd)
source_dir=${LIBCOAP_SOURCE:-/private/tmp/libcoap-qblock-reference}
build_dir=${LIBCOAP_BUILD:-/private/tmp/libcoap-qblock-build}
output_dir=${QBLOCK_INTEROP_OUTPUT:-/private/tmp/qblock-interop-$(date +%Y%m%d-%H%M%S)}
pin=c63c8f7cb7f248a4992539529b9e1b691962f29a
actual=$(rtk proxy git -C "$source_dir" rev-parse HEAD)
if [[ "$actual" != "$pin" ]] || [[ -n "$(rtk proxy git -C "$source_dir" status --porcelain --untracked-files=all)" ]]; then
  echo "Expected clean libcoap source at $pin; got $actual" >&2
  exit 1
fi
rtk proxy mkdir -p "$build_dir"
rtk proxy cmake -S "$source_dir" -B "$build_dir" \
  -DENABLE_Q_BLOCK=ON -DENABLE_EXAMPLES=ON -DENABLE_TESTS=OFF \
  -DENABLE_DOCS=OFF -DENABLE_DTLS=OFF -DENABLE_TCP=OFF \
  -DENABLE_WS=OFF -DENABLE_OSCORE=OFF -DBUILD_SHARED_LIBS=OFF \
  -DCMAKE_BUILD_TYPE=Debug > "$build_dir/configure.log" 2>&1
rtk proxy cmake --build "$build_dir" --parallel 4 > "$build_dir/build.log" 2>&1
rtk proxy cc -std=gnu11 -g -I"$build_dir" -I"$build_dir/include" -I"$source_dir/include" \
  "$repo/tests/interop/qblock/libcoap-server.c" "$build_dir/libcoap-3.a" -o "$build_dir/libcoap-fixture-server"
cd "$repo"
rtk proxy env GOCACHE="${GOCACHE:-/private/tmp/go-coap-con-build}" go build -o "$build_dir/go-fixture" ./tests/interop/qblock/fixture
rtk proxy python3 -m unittest discover -s tests/interop/qblock -p test_wire.py
echo "Evidence: $output_dir"
rtk proxy python3 tests/interop/qblock/harness.py --build "$build_dir" --fixture "$build_dir/go-fixture" --output "$output_dir" --server-fixture
