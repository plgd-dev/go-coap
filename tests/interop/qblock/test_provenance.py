import tempfile
import unittest
from pathlib import Path

from provenance import fingerprint, dependency_files


class ProvenanceTest(unittest.TestCase):
    def test_fingerprint_tracks_relevant_file_content_and_path(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / 'conn.go'
            source.write_text('package client\n')
            original = fingerprint({'repo/udp/client/conn.go': source})
            self.assertEqual(original, fingerprint({'repo/udp/client/conn.go': source}))
            source.write_text('package client\n// changed behavior\n')
            self.assertNotEqual(original['sha256'], fingerprint({'repo/udp/client/conn.go': source})['sha256'])
            self.assertNotEqual(original['sha256'], fingerprint({'repo/udp/client/other.go': source})['sha256'])

    def test_fingerprint_is_order_independent_and_excludes_unselected_docs(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            a, b, docs = root / 'a.go', root / 'b.go', root / 'user.md'
            a.write_bytes(b'a')
            b.write_bytes(b'b')
            docs.write_bytes(b'user-owned')
            before = fingerprint({'repo/a.go': a, 'repo/b.go': b})
            docs.write_bytes(b'changed user-owned')
            self.assertEqual(before, fingerprint({'repo/b.go': b, 'repo/a.go': a}))

    def test_actual_go_dependency_selection_tracks_source_and_excludes_workspace_docs(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            fixture = root / 'tests/interop/qblock/fixture'
            library = root / 'udp/client'
            fixture.mkdir(parents=True)
            library.mkdir(parents=True)
            (root / 'go.mod').write_text('module example.com/provenance\n\ngo 1.22\n')
            (fixture / 'main.go').write_text('package main\nimport _ "example.com/provenance/udp/client"\nfunc main() {}\n')
            source = library / 'conn.go'
            source.write_text('package client\nconst Behavior = 1\n')
            docs = root / 'user.md'
            docs.write_text('user owned\n')
            files, _ = dependency_files(root)
            self.assertIn('repo/udp/client/conn.go', files)
            self.assertNotIn('repo/user.md', files)
            before = fingerprint(files)
            docs.write_text('changed docs\n')
            self.assertEqual(before, fingerprint(dependency_files(root)[0]))
            source.write_text('package client\nconst Behavior = 2\n')
            self.assertNotEqual(before['sha256'], fingerprint(dependency_files(root)[0])['sha256'])


if __name__ == '__main__':
    unittest.main()
