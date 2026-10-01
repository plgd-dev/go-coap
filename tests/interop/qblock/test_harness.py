import unittest

from harness import validate_response_status


class ResponseStatusTest(unittest.TestCase):
    def test_get_content_and_upload_changed_are_required(self):
        self.assertEqual(validate_response_status([{'direction': 'server_to_client', 'wire_hex': '51450001aa'}], 'GET'), 69)
        for method in ('POST', 'PUT'):
            self.assertEqual(validate_response_status([{'direction': 'server_to_client', 'wire_hex': '51440001aa'}], method), 68)

    def test_wrong_success_class_status_is_rejected(self):
        # 2.01 Created is success-class but wrong for these fixed existing echo resources.
        for method in ('GET', 'POST', 'PUT'):
            with self.subTest(method=method), self.assertRaises(AssertionError):
                validate_response_status([{'direction': 'server_to_client', 'wire_hex': '51410001aa'}], method)


if __name__ == '__main__':
    unittest.main()
