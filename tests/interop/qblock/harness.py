"""Local/manual independent-peer runner and transparent UDP evidence capture."""
import argparse
import json
import hashlib
import platform
import shutil
import select
import socket
import subprocess
import threading
import time
from pathlib import Path

from wire import audit, decode
from provenance import verify


class Relay:
    def __init__(self, target, trace, fault=None):
        self.socket = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.socket.bind(('127.0.0.1', 0))
        self.target = ('127.0.0.1', target)
        self.client = None
        self.stop = threading.Event()
        self.records = []
        self.trace = trace
        self.fault = fault
        self.dropped = False
        self.thread = threading.Thread(target=self.run)
        self.thread.start()

    @property
    def port(self):
        return self.socket.getsockname()[1]

    def run(self):
        with self.trace.open('w') as file:
            while not self.stop.is_set():
                if not select.select([self.socket], [], [], 0.05)[0]:
                    continue
                raw, addr = self.socket.recvfrom(65535)
                if addr == self.target:
                    direction, dest = 'server_to_client', self.client
                else:
                    direction, dest = 'client_to_server', self.target
                    self.client = addr
                decision = 'forward'
                if self.fault and not self.dropped and direction == 'server_to_client':
                    p = decode(raw)
                    if p['type'] == 'NON' and any(o['number'] == 31 and int(o['hex'] or '0', 16) >> 4 == 3 for o in p['options']):
                        self.dropped = True
                        decision = 'drop-q2-block-3-first-occurrence'
                record = {'time_ns': time.monotonic_ns(), 'direction': direction, 'wire_hex': raw.hex(), 'decision': decision}
                self.records.append(record)
                file.write(json.dumps(record) + '\n')
                file.flush()
                if dest is not None and decision == 'forward':
                    self.socket.sendto(raw, dest)

    def close(self):
        self.stop.set()
        self.thread.join()
        self.socket.close()


def free_port():
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def validate_response_status(records, method):
    expected = 69 if method == 'GET' else 68  # Content / Changed for fixed existing echo resources.
    statuses = [p['code'] for r in records if r['direction'] == 'server_to_client'
                for p in [decode(bytes.fromhex(r['wire_hex']))]
                if p['type'] == 'NON' and 64 <= p['code'] < 96 and p['code'] != 95]
    assert statuses, 'no terminal NON payload response'
    assert all(code == expected for code in statuses), f'{method}: expected response code {expected}, observed {statuses}'
    return expected


def run_case(args, direction, method, payload, fault=False):
    case = args.output / f'{direction}-{method.lower()}{"-loss" if fault else ""}'
    case.mkdir()
    port = free_port()
    libclient = str(args.build / 'coap-client')
    if direction == 'go-client' and args.server_fixture:
        server = [str(args.build / 'libcoap-fixture-server'), str(port)]
        if args.transport == 'dtls':
            server += ['dtls']
        path = '/fixture'
    elif direction == 'go-client':
        server = [str(args.build / 'coap-server'), '-A', '127.0.0.1', '-p', str(port), '-L', '7', '-b', '64', '-d', '10', '-e', '-v', '8']
        path = '/upload' if method == 'POST' else '/example_data'
    else:
        server = [str(args.fixture), '-role', 'server', '-addr', f'127.0.0.1:{port}', '-input', str(args.output / 'body.bin'), '-transport', args.transport]
        path = '/get' if method == 'GET' else '/upload'
    (case / 'server-command.json').write_text(json.dumps(server, indent=2))
    relay = None
    with (case / 'server.log').open('w') as server_log:
        proc = subprocess.Popen(server, stdout=server_log, stderr=subprocess.STDOUT)
        try:
            time.sleep(0.2)
            if proc.poll() is not None:
                raise RuntimeError(f'server exited {proc.returncode}')
            if direction == 'go-client' and method == 'POST' and not args.server_fixture:
                seed = [libclient, '-m', 'put', '-e', 'seed', '-B', '5', f'coap://127.0.0.1:{port}/upload']
                (case / 'seed-command.json').write_text(json.dumps(seed, indent=2))
                with (case / 'seed.log').open('w') as log:
                    subprocess.run(seed, stdout=log, stderr=subprocess.STDOUT, check=True, timeout=8)
            relay = Relay(port, case / 'wire.jsonl', fault)
            out = case / 'response.bin'
            if direction == 'go-client':
                command = [str(args.fixture), '-addr', f'127.0.0.1:{relay.port}', '-method', method, '-path', path,
                           '-input', str(args.output / 'body.bin'), '-output', str(out), '-transport', args.transport]
                if args.server_fixture:
                    command += ['-probe', '/fixture']
            else:
                # 1=library block handling, 2=single assembled body, 4=try Q-Block.
                command = [libclient, '-L', '7', '-N', '-b', '64', '-B', '20', '-v', '8', '-m', method.lower(), '-o', str(out)]
                if args.explicit_token:
                    command += ['-T', 'interop']
                if args.transport == 'dtls':
                    command += ['-u', 'qblock-interop', '-k', 'qblock-interop-local-key', '-V', '7']
                if method != 'GET':
                    command += ['-f', str(args.output / 'body.bin'), '-t', '0']
                scheme = 'coaps' if args.transport == 'dtls' else 'coap'
                command += [f'{scheme}://127.0.0.1:{relay.port}{path}']
            (case / 'client-command.json').write_text(json.dumps(command, indent=2))
            with (case / 'client.log').open('w') as client_log:
                result = subprocess.run(command, stdout=client_log, stderr=subprocess.STDOUT, timeout=25)
            time.sleep(0.1)
            relay.close()
            records = relay.records
            dropped = relay.dropped
            relay = None
            if result.returncode:
                raise RuntimeError(f'client exited {result.returncode}; see client.log')
            expected = payload
            if direction == 'go-client' and method == 'GET' and not args.server_fixture:
                expected = bytes(ord('a') + (i // 10) % 26 if i % 10 == 0 else ord('0') + i % 10 for i in range(1500))
            actual = out.read_bytes() if out.exists() else b''
            assert actual == expected, f'response mismatch: {len(actual)} bytes; expected {len(expected)}'
            if args.transport == 'udp':
                status = validate_response_status(records, method)
                report = audit([r for r in records if r['decision'] == 'forward'], method, expected, payload if method != 'GET' else None)
                report['expected_response_code'] = status
                if fault:
                    assert dropped, 'configured Q2 block3 fault was not exercised'
                    controls = [decode(bytes.fromhex(r['wire_hex'])) for r in records if r['direction'] == 'client_to_server']
                    repair = [p for p in controls if any(o['number'] == 31 and int(o['hex'] or '0', 16) >> 4 == 3 for o in p['options'])]
                    assert repair, 'lost block did not cause explicit Q2 block3 recovery request'
                    report['fault'] = 'drop first server_to_client NON Q2 block3'
                    report['repair_requests'] = len(repair)
            else:
                log_text = (case / ('server.log' if direction == 'go-client' else 'client.log')).read_text()
                assert 'Q-Block2:' in log_text, 'no libcoap Q-Block2 diagnostic evidence'
                if method != 'GET':
                    assert 'Q-Block1:' in log_text, 'no libcoap Q-Block1 diagnostic evidence'
                report = {'method': method, 'packets': len(records), 'transport': 'dtls-psk',
                          'wire_audit': 'encrypted datagrams retained; plaintext option evidence from independent libcoap diagnostics only'}
            report['body_bytes'] = len(actual)
            (case / 'audit.json').write_text(json.dumps(report, indent=2))
            return {'case': case.name, 'status': 'PASS', 'body_bytes': len(actual), 'packets': report['packets']}
        except Exception as exc:
            return {'case': case.name, 'status': 'FAIL', 'error': str(exc)}
        finally:
            if relay:
                relay.close()
            proc.terminate()
            try:
                proc.wait(timeout=3)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--build', type=Path, required=True)
    parser.add_argument('--fixture', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--server-fixture', action='store_true', help='use public libcoap API server handler with explicit Size2')
    parser.add_argument('--explicit-token', action='store_true', help='libcoap client uses nonempty token')
    parser.add_argument('--transport', choices=['udp', 'dtls'], default='udp')
    parser.add_argument('--provenance', type=Path, help='pre-build source capture directory (required by the runners)')
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=False)
    payload = bytes(33 + i % 90 for i in range(1500))
    (args.output / 'body.bin').write_bytes(payload)
    (args.output / 'configuration.json').write_text(json.dumps({'server_fixture': args.server_fixture, 'explicit_token': args.explicit_token, 'transport': args.transport}, indent=2))
    binaries = [args.fixture, args.build / 'coap-client', args.build / ('libcoap-fixture-server' if args.server_fixture else 'coap-server')]
    manifest = {'libcoap_pin': 'c63c8f7cb7f248a4992539529b9e1b691962f29a', 'platform': platform.platform(),
                'go_head': subprocess.check_output(['git', 'rev-parse', 'HEAD'], text=True).strip(),
                'binaries_sha256': {str(p): hashlib.sha256(p.read_bytes()).hexdigest() for p in binaries}}
    repo = Path(__file__).resolve().parents[3]
    if args.provenance:
        captured = verify(repo, args.provenance)
        shutil.copytree(args.provenance, args.output / 'provenance')
        manifest['source_sha256'] = captured['source']['sha256']
        manifest['source_manifest'] = 'provenance/source-manifest.json'
    manifest['fixture_build_info'] = subprocess.check_output(['go', 'version', '-m', str(args.fixture)], text=True)
    (args.output / 'manifest.json').write_text(json.dumps(manifest, indent=2))
    for name in ['configure.log', 'build.log', 'CMakeCache.txt']:
        path = args.build / name
        if path.exists():
            (args.output / name).write_bytes(path.read_bytes())
    results = []
    for direction in ['go-client', 'libcoap-client']:
        for method in ['GET', 'PUT', 'POST']:
            result = run_case(args, direction, method, payload)
            results.append(result)
            print(json.dumps(result), flush=True)
        if args.transport == 'udp':
            result = run_case(args, direction, 'GET', payload, fault=True)
            results.append(result)
            print(json.dumps(result), flush=True)
    (args.output / 'results.json').write_text(json.dumps(results, indent=2))
    if args.provenance:
        verify(repo, args.provenance)
        manifest['source_unchanged_after_run'] = True
        (args.output / 'manifest.json').write_text(json.dumps(manifest, indent=2))
    return 0 if all(r['status'] == 'PASS' for r in results) else 1


if __name__ == '__main__':
    raise SystemExit(main())
