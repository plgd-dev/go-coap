import unittest

from wire import decode, audit


class WireTest(unittest.TestCase):
    def test_preserves_repeated_q_options_and_payload(self):
        # NON, POST, MID 0x1234, token aa; Q-Block1=8, repeated=24; payload xyz.
        p = decode(bytes.fromhex('51021234aad106080118ff78797a'))
        self.assertEqual(p['type'], 'NON')
        self.assertEqual(p['options'], [{'number': 19, 'hex': '08'}, {'number': 19, 'hex': '18'}])
        self.assertEqual(p['payload_hex'], '78797a')

    def test_rejects_truncated_extended_option(self):
        with self.assertRaises(ValueError):
            decode(bytes.fromhex('40010001e1'))

    def test_audit_rejects_classic_fallback(self):
        with self.assertRaises(AssertionError):
            audit([{'direction': 'client_to_server', 'wire_hex': '51010001aad10a00'}], 'GET')

    def test_audit_rejects_corrupt_body_and_metadata(self):
        # Hand encoded two-block GET exchange, ETag 01, Size2=17, Q2 /16.
        records = [
            {'direction': 'client_to_server', 'wire_hex': '41010000bbd012'},
            {'direction': 'server_to_client', 'wire_hex': '61450000bb4101d10b113108ff' + '61' * 16},
            {'direction': 'client_to_server', 'wire_hex': '51010001aab178d10708d4f801010101'},
            {'direction': 'server_to_client', 'wire_hex': '51450002aa4101d10b113108ff' + '61' * 16},
            {'direction': 'server_to_client', 'wire_hex': '51450003aa4101d10b113110ff62'},
        ]
        audit(records, 'GET', expected_response=b'a' * 16 + b'b')
        # Current decoder can inspect corrupted messages; the audit must reject
        # missing fragment coverage, metadata changes and non-NON data.
        for index, offset, replacement in [(4, 0, '41'), (4, 12, '02'), (4, 18, '12'), (4, 26, '63')]:
            changed = [dict(r) for r in records]
            raw = changed[index]['wire_hex']
            changed[index]['wire_hex'] = raw[:offset] + replacement + raw[offset + 2:]
            with self.subTest(index=index, offset=offset), self.assertRaises(AssertionError):
                audit(changed, 'GET', expected_response=b'a' * 16 + b'b')


if __name__ == '__main__':
    unittest.main()
