"""Download a pinned, checksum-verified official Linux Compose CLI plugin."""
import hashlib
from pathlib import Path
import sys
import urllib.request


VERSION = 'v5.6.0'
SHA256 = {
    'x86_64': '40343e21ca777173e69cff5dbafeb37c6f81f3b0d57d9e597f036e95eb63e76a',
    'aarch64': '733ec76717ceb59052a9609b9dadfb523b2df8eab57a54212872d10a58078ea2',
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
