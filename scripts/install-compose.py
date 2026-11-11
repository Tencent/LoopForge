"""Download a pinned, checksum-verified official Linux Compose CLI plugin."""
import hashlib
from pathlib import Path
import sys
import urllib.request


VERSION = 'v5.6.1'
SHA256 = {
    'x86_64': '47737f4236e6d3ccea7291995203f8f018058783b4c3a9469119811a0b5db8c0',
    'aarch64': '42cfa75f51ad0ccf290bad37899e0038a8c10a0069e85233ecc1d53e3ff1347d',
}


def download(destination, architecture, opener=urllib.request.urlopen):
    architecture = {'amd64': 'x86_64', 'arm64': 'aarch64'}.get(architecture, architecture)
    if architecture not in SHA256:
        raise ValueError('Automatic Compose installation supports Linux x86_64 and aarch64. Install the Docker Compose plugin for this architecture, then rerun the installer.')
    destination = Path(destination)
    partial = destination.with_suffix('.download')
    url = 'https://github.com/docker/compose/releases/download/{}/docker-compose-linux-{}'.format(VERSION, architecture)
    try:
        digest = hashlib.sha256()
        with opener(url, timeout=120) as response, partial.open('wb') as output:
            while True:
                chunk = response.read(1024 * 1024)
                if not chunk:
                    break
                digest.update(chunk)
                output.write(chunk)
        if digest.hexdigest() != SHA256[architecture]:
            raise ValueError('Docker Compose checksum mismatch; the downloaded file will not be installed.')
        partial.replace(destination)
    finally:
        try:
            partial.unlink()
        except FileNotFoundError:
            pass


if __name__ == '__main__':
    try:
        download(sys.argv[1], sys.argv[2])
    except (OSError, ValueError) as error:
        raise SystemExit('Could not install Docker Compose: ' + str(error))
