import ipaddress
import os
import socket
import sys
import shlex
from pathlib import Path
from urllib.parse import urlsplit


def container_host():
    if Path('/.dockerenv').exists() or Path('/run/.containerenv').exists():
        return True
    try:
        return any(marker in Path('/proc/1/cgroup').read_text() for marker in ('/docker/', '/kubepods/', '/lxc/'))
    except OSError:
        return False


def access_addresses(port, environ=os.environ):
    bind = environ.get('LOOPFORGE_BIND', '0.0.0.0')
    public = environ.get('LOOPFORGE_PUBLIC_URL', '').rstrip('/')
    if public:
        parsed = urlsplit(public)
        if parsed.scheme not in ('http', 'https') or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment or parsed.path not in ('', '/'):
            raise ValueError('LOOPFORGE_PUBLIC_URL must be an HTTP(S) URL without credentials, query or fragment')
        parsed.port
        try:
            address = ipaddress.ip_address(parsed.hostname)
        except ValueError:
            address = None
        if parsed.hostname in ('0.0.0.0', '::') or address and (address.is_unspecified or address.is_link_local):
            raise ValueError('Use a reachable server IP/domain, not a listening or link-local address')
        if (parsed.hostname == 'localhost' or address and address.is_loopback) and bind not in ('127.0.0.1', 'localhost', '::1'):
            raise ValueError('Team access cannot use localhost/127.0.0.1; enter the server IP/domain users visit')
        if bind in ('127.0.0.1', 'localhost', '::1'):
            return [public], 'Loopback binding: the public URL requires a reverse proxy on this server.'
        return [public], 'Allow the service port in the cloud security group/firewall, or route it through your HTTPS reverse proxy.'
    if bind in ('127.0.0.1', 'localhost', '::1'):
        host = f'[{bind}]' if ':' in bind else bind
        return [f'http://{host}:{port}'], 'Local access only. Set LOOPFORGE_BIND=0.0.0.0 and LOOPFORGE_PUBLIC_URL in .env for team access.'
    if container_host():
        return [], 'Installation is inside a container; open the host IP and published port.'
    if bind not in ('0.0.0.0', '::', '[::]'):
        hosts = [bind.strip('[]')]
    else:
        hosts = []
        try:
            with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as probe:
                probe.connect(('192.0.2.1', 80))
                hosts.append(probe.getsockname()[0])
        except OSError:
            pass
        try:
            hosts.extend(socket.gethostbyname_ex(socket.gethostname())[2])
        except OSError:
            pass
        hosts = list(dict.fromkeys(host for host in hosts if not ipaddress.ip_address(host).is_loopback and not ipaddress.ip_address(host).is_unspecified and not ipaddress.ip_address(host).is_link_local))
    addresses = [f'http://[{host}]:{port}' if ':' in host else f'http://{host}:{port}' for host in hosts]
    return addresses, 'On cloud hosts behind NAT, open the public IP instead and allow the port in the security group/firewall.'


def prepare_address(port, environ=os.environ):
    access_addresses(port, environ)
    public = environ.get('LOOPFORGE_PUBLIC_URL', '').rstrip('/')
    if public:
        config = Path('.env')
        lines = config.read_text().splitlines()
        replacement = 'LOOPFORGE_PUBLIC_URL='+shlex.quote(public)
        updated = [replacement if line.startswith('LOOPFORGE_PUBLIC_URL=') else line for line in lines]
        if not any(line.startswith('LOOPFORGE_PUBLIC_URL=') for line in lines):
            updated.append(replacement)
        if updated != lines:
            with config.open('w') as handle:
                handle.write('\n'.join(updated)+'\n')


if __name__ == '__main__':
    if sys.argv[1] == '--prepare':
        try:
            prepare_address(sys.argv[2])
        except (ValueError, EOFError) as error:
            sys.exit(str(error))
        sys.exit(0)
    addresses, note = access_addresses(sys.argv[1])
    print('\n✓ LoopForge is running')
    for address in addresses:
        print(f'  {"Access" if os.environ.get("LOOPFORGE_PUBLIC_URL") else "Server address (may be LAN-only)"}: {address}')
    if not addresses:
        print(f'  Listening on {os.environ.get("LOOPFORGE_BIND", "0.0.0.0")}:{sys.argv[1]}; open http://SERVER_IP:{sys.argv[1]}.')
    print(f'  {note}')
