import ipaddress
import os
import socket
import sys
from urllib.parse import urlsplit


def access_addresses(port, environ=os.environ):
    bind = environ.get('LOOPFORGE_BIND', '0.0.0.0')
    public = environ.get('LOOPFORGE_PUBLIC_URL', '').rstrip('/')
    if public:
        parsed = urlsplit(public)
        if parsed.scheme not in ('http', 'https') or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment or parsed.path not in ('', '/'):
            raise ValueError('LOOPFORGE_PUBLIC_URL must be an HTTP(S) URL without credentials, query or fragment')
        parsed.port
        if bind in ('127.0.0.1', 'localhost', '::1'):
            return [public], 'Loopback binding: the public URL requires a reverse proxy on this server.'
        return [public], 'Allow the service port in the cloud security group/firewall, or route it through your HTTPS reverse proxy.'
    if bind in ('127.0.0.1', 'localhost', '::1'):
        host = f'[{bind}]' if ':' in bind else bind
        return [f'http://{host}:{port}'], 'Local access only. Set LOOPFORGE_BIND=0.0.0.0 and LOOPFORGE_PUBLIC_URL in .env for team access.'
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
        hosts = list(dict.fromkeys(host for host in hosts if not ipaddress.ip_address(host).is_loopback and not ipaddress.ip_address(host).is_unspecified))
    addresses = [f'http://[{host}]:{port}' if ':' in host else f'http://{host}:{port}' for host in hosts]
    return addresses, 'Cloud NAT public addresses cannot be inferred reliably. Set LOOPFORGE_PUBLIC_URL to your public IP/domain and allow the service port in the security group/firewall.'


if __name__ == '__main__':
    addresses, note = access_addresses(sys.argv[1])
    print('\n✓ LoopForge is running')
    for address in addresses:
        print(f'  {"Access" if os.environ.get("LOOPFORGE_PUBLIC_URL") else "Server address (may be LAN-only)"}: {address}')
    if not addresses:
        print('  Set LOOPFORGE_PUBLIC_URL in .env to your reachable server IP/domain.')
    print(f'  {note}')
