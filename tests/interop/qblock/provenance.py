"""Capture path-stable source provenance before building the interop fixture."""
import argparse
import hashlib
import json
import shutil
import subprocess
from pathlib import Path


def fingerprint(files):
    entries = []
    for name, path in sorted(files.items()):
        raw = path.read_bytes()
        entries.append({'path': name, 'bytes': len(raw), 'sha256': hashlib.sha256(raw).hexdigest()})
    encoded = json.dumps(entries, sort_keys=True, separators=(',', ':')).encode()
    return {'sha256': hashlib.sha256(encoded).hexdigest(), 'files': entries}


def command(repo, *args):
    return subprocess.check_output(args, cwd=repo, text=True)


def dependency_files(repo):
    raw = command(repo, 'go', 'list', '-deps', '-json', './tests/interop/qblock/fixture')
    packages = []
    decoder = json.JSONDecoder()
    while raw.strip():
        package, end = decoder.raw_decode(raw.lstrip())
        packages.append(package)
        raw = raw.lstrip()[end:]
    files, modules = {}, {}
    fields = ('GoFiles', 'CgoFiles', 'CFiles', 'CXXFiles', 'MFiles', 'HFiles', 'FFiles', 'SFiles', 'SysoFiles', 'EmbedFiles')
    for package in packages:
        if package.get('Standard'):
            continue
        directory = Path(package['Dir']).resolve()
        module = package.get('Module', {})
        identity = module.get('Path', '') + '@' + module.get('Version', 'main')
        if module:
            modules[identity] = {k: module[k] for k in ('Path', 'Version', 'Sum', 'GoModSum', 'GoVersion') if k in module}
        for field in fields:
            for name in package.get(field, []):
                path = (directory / name).resolve()
                if path.is_relative_to(repo):
                    key = 'repo/' + path.relative_to(repo).as_posix()
                else:
                    module_root = Path(module.get('Dir', directory)).resolve()
                    key = 'modules/' + identity + '/' + path.relative_to(module_root).as_posix()
                files[key] = path
        go_mod = module.get('GoMod')
        if go_mod:
            path = Path(go_mod).resolve()
            if not path.is_relative_to(repo):
                files['modules/' + identity + '/go.mod'] = path
    for name in ('go.mod', 'go.sum', 'go.work', 'go.work.sum'):
        path = repo / name
        if path.is_file():
            files['repo/' + name] = path
    # Explicit harness inputs only: no unrelated docs, tooling metadata, ledger
    # evidence or untracked private files are included.
    harness = repo / 'tests/interop/qblock'
    for pattern in ('*.py', '*.sh', '*.c', '*.md', '.gitignore', 'fixture/*.go'):
        for path in harness.glob(pattern):
            files['repo/' + path.relative_to(repo).as_posix()] = path
    return files, [modules[k] for k in sorted(modules)]


def capture(repo, output):
    files, modules = dependency_files(repo)
    digest = fingerprint(files)
    output.mkdir(parents=True, exist_ok=False)
    sources = output / 'source'
    for name, path in files.items():
        target = sources / name
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(path, target)
    if fingerprint({name: sources / name for name in files}) != digest:
        raise RuntimeError('source changed while snapshot was copied; capture again')
    scoped = [name.removeprefix('repo/') for name in files if name.startswith('repo/')]
    patch = command(repo, 'git', 'diff', '--binary', 'HEAD', '--', *sorted(scoped))
    (output / 'dirty-source.diff').write_text(patch)
    environment = json.loads(command(repo, 'go', 'env', '-json', 'GOVERSION', 'GOOS', 'GOARCH', 'GOAMD64', 'GOARM64', 'CGO_ENABLED', 'GOFLAGS', 'GOTOOLCHAIN', 'CC', 'CXX'))
    result = {'schema': 1, 'head': command(repo, 'git', 'rev-parse', 'HEAD').strip(),
              'source': digest, 'dependency_modules': modules, 'go_environment': environment,
              'toolchain': command(repo, 'go', 'version').strip(),
              'dirty_diff_sha256': hashlib.sha256(patch.encode()).hexdigest(),
              'scope': 'actual nonstdlib fixture dependency inputs, module manifests, and explicit interop harness files; stdlib identified by toolchain',
              'local_paths': {name: str(path) for name, path in files.items()}}
    (output / 'source-manifest.json').write_text(json.dumps(result, indent=2))
    return result


def verify(repo, output):
    recorded = json.loads((output / 'source-manifest.json').read_text())
    files, _ = dependency_files(repo)
    current = fingerprint(files)
    if current != recorded['source']:
        raise RuntimeError('relevant source changed after provenance capture; rebuild before claiming interop')
    snapshot = fingerprint({entry['path']: output / 'source' / entry['path'] for entry in recorded['source']['files']})
    if snapshot != recorded['source']:
        raise RuntimeError('retained source snapshot differs from recorded fingerprint')
    return recorded


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('action', choices=['capture', 'verify'])
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    repo = Path(__file__).resolve().parents[3]
    result = capture(repo, args.output) if args.action == 'capture' else verify(repo, args.output)
    print(result['source']['sha256'])


if __name__ == '__main__':
    main()
