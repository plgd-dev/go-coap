"""Independent RFC 7252 UDP wire parser; no go-coap code or log decoding."""
import argparse
import json
import hashlib
from pathlib import Path


def decode(raw):
    if len(raw) < 4 or raw[0] >> 6 != 1:
        raise ValueError('invalid CoAP header')
    token_len = raw[0] & 15
    if token_len > 8 or len(raw) < 4 + token_len:
        raise ValueError('invalid token')
    pos = 4 + token_len
    options, number = [], 0

    def extended(n):
        nonlocal pos
        count = {13: 1, 14: 2}.get(n, 0)
        if n == 15 or pos + count > len(raw):
            raise ValueError('invalid or truncated option')
        value = int.from_bytes(raw[pos:pos + count], 'big') if count else n
        pos += count
        return value + ({13: 13, 14: 269}.get(n, 0))

    payload = b''
    while pos < len(raw):
        header = raw[pos]
        pos += 1
        if header == 255:
            if pos == len(raw):
                raise ValueError('empty payload marker')
            payload = raw[pos:]
            break
        number += extended(header >> 4)
        size = extended(header & 15)
        if pos + size > len(raw):
            raise ValueError('truncated option value')
        options.append({'number': number, 'hex': raw[pos:pos + size].hex()})
        pos += size
    return {'type': ['CON', 'NON', 'ACK', 'RST'][(raw[0] >> 4) & 3],
            'code': raw[1], 'mid': int.from_bytes(raw[2:4], 'big'),
            'token': raw[4:4 + token_len].hex(), 'options': options,
            'payload_hex': payload.hex()}


def audit(records, method, expected_response=None, expected_upload=None):
    packets = [dict(r, decoded=decode(bytes.fromhex(r['wire_hex']))) for r in records]
    code = {'GET': 1, 'POST': 2, 'PUT': 3}[method]
    requests = [p['decoded'] for p in packets if p['direction'] == 'client_to_server' and p['decoded']['code'] == code]
    probes = [p['decoded'] for p in packets if p['direction'] == 'client_to_server' and p['decoded']['type'] == 'CON' and p['decoded']['code'] == 1 and any(o['number'] == 31 and int(o['hex'] or '0', 16) == 0 for o in p['decoded']['options'])]
    assert probes, 'missing explicit CON Q capability probe'
    probe_tokens = {p['token'] for p in probes}
    data_requests = [p['decoded'] for p in packets if p['direction'] == 'client_to_server' and p['decoded']['code'] in (1, 2, 3) and p['decoded']['token'] not in probe_tokens]
    assert all(p['type'] == 'NON' for p in data_requests), 'payload or repair request must be NON'
    probe_replies = [p['decoded'] for p in packets if p['direction'] == 'server_to_client' and p['decoded']['token'] in probe_tokens and p['decoded']['code'] == 69]
    assert probe_replies, 'missing positive capability response'
    assert all(p['type'] == 'ACK' for p in probe_replies), 'fixture probe response must be ACK'
    assert all(all(sum(o['number'] == n for o in p['options']) == 1 for n in (4, 28, 31)) for p in probe_replies), 'probe missing required Q2 metadata'
    for p in packets:
        assert not any(o['number'] in (23, 27) for o in p['decoded']['options']), 'classic Block fallback or mixing'
    # Capability ACK is checked separately from all NON payload traffic.
    payload = [p for p in requests if any(o['number'] == (31 if method == 'GET' else 19) for o in p['options']) and p['type'] == 'NON']
    assert payload, f'{method}: no NON Q payload request (classic fallback is not interop)'
    responses = [p['decoded'] for p in packets if p['direction'] == 'server_to_client' and p['decoded']['code'] in (65, 68, 69) and p['decoded']['token'] not in probe_tokens]
    q2 = [p for p in responses if any(o['number'] == 31 for o in p['options']) and p['payload_hex']]
    assert q2, f'{method}: no Q-Block2 response body'
    assert all(p['type'] == 'NON' for p in q2), 'Q response body must be NON'
    assert all(sum(o['number'] == 292 for o in p['options']) == 1 for p in data_requests), 'one Request-Tag required'
    tags = {next(o['hex'] for o in p['options'] if o['number'] == 292) for p in data_requests}
    assert len(tags) == 1, 'Request-Tag changed'
    request_tokens = {p['token'] for p in requests}
    assert all(p['token'] in request_tokens for p in q2), 'response token does not correlate to a request'

    def reassemble(messages, option, size_option, expected):
        sizes, etags, formats, szxs, fragments = set(), set(), set(), set(), {}
        final = None
        for p in messages:
            values = [o for o in p['options'] if o['number'] == option]
            assert len(values) == 1, 'data has duplicate block option'
            size_values = [o for o in p['options'] if o['number'] == size_option]
            assert len(size_values) == 1, 'missing or repeated Size option'
            sizes.add(int(size_values[0]['hex'] or '0', 16))
            if option == 31:
                etag_values = [o for o in p['options'] if o['number'] == 4]
                assert len(etag_values) == 1 and 0 < len(etag_values[0]['hex']) <= 16, 'missing or invalid ETag'
                etags.add(etag_values[0]['hex'])
            formats.add(tuple(o['hex'] for o in p['options'] if o['number'] == 12))
            block = int(values[0]['hex'] or '0', 16)
            number, more, szx = block >> 4, bool(block & 8), block & 7
            assert szx <= 6, 'invalid UDP SZX'
            szxs.add(szx)
            raw = bytes.fromhex(p['payload_hex'])
            assert len(raw) <= 16 << szx and (not more or len(raw) == 16 << szx), 'invalid fragment length'
            if number in fragments:
                assert fragments[number] == raw, 'conflicting duplicate fragment'
            fragments[number] = raw
            if not more:
                assert final is None or final == number, 'final block changed'
                final = number
        assert len(sizes) == len(szxs) == len(formats) == 1, 'Size/SZX/content format changed'
        if option == 31:
            assert len(etags) == 1, 'ETag changed'
        assert final is not None and set(fragments) == set(range(final + 1)), 'incomplete fragment coverage'
        body = b''.join(fragments[n] for n in range(final + 1))
        assert len(body) == next(iter(sizes)), 'announced size does not match reconstructed body'
        if expected is not None:
            assert body == expected, 'reconstructed body differs from expected bytes'
        return {'bytes': len(body), 'sha256': hashlib.sha256(body).hexdigest(), 'blocks': final + 1}

    response_body = reassemble(q2, 31, 28, expected_response)
    upload_body = None
    if method != 'GET':
        upload_body = reassemble([p for p in payload if p['payload_hex']], 19, 60, expected_upload)
        blocks = [int(o['hex'] or '0', 16) >> 4 for p in payload for o in p['options'] if o['number'] == 19]
        assert len(set(blocks)) > 1, f'{method}: upload did not span multiple Q-Block1 blocks'
    q2_blocks = [int(o['hex'] or '0', 16) >> 4 for p in q2 for o in p['options'] if o['number'] == 31]
    assert len(set(q2_blocks)) > 1, f'{method}: response did not span multiple Q-Block2 blocks'
    return {'method': method, 'packets': len(packets), 'non_q_requests': len(payload),
            'q2_response_blocks': sorted(set(q2_blocks)), 'response_body': response_body,
            'upload_body': upload_body, 'request_tag': next(iter(tags)), 'decoded_packets': packets}


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('trace', type=Path)
    parser.add_argument('--method', choices=['GET', 'POST', 'PUT'], required=True)
    parser.add_argument('--response', type=Path, help='expected response file for exact reconstruction check')
    parser.add_argument('--upload', type=Path, help='expected request file for exact reconstruction check')
    args = parser.parse_args()
    records = [json.loads(line) for line in args.trace.read_text().splitlines()]
    records = [r for r in records if r.get('decision', 'forward') == 'forward']
    print(json.dumps(audit(records, args.method,
                           args.response.read_bytes() if args.response else None,
                           args.upload.read_bytes() if args.upload else None), indent=2))
