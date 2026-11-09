import ipaddress
import os
import socket
import queue
import sys
import shlex
import threading
import time
import urllib.request
from pathlib import Path
from urllib.parse import urlsplit

# Link-local cloud metadata services: a NAT-ed cloud host's own interfaces only
# carry its private address. Nothing is sent outside the provider's network.
PUBLIC_IPV4_METADATA = (
    ('http://169.254.0.23/latest/meta-data/public-ipv4', {}),  # Tencent Cloud
    ('http://100.100.100.200/latest/meta-data/eipv4', {}),  # Alibaba Cloud
    ('http://169.254.169.254/latest/meta-data/public-ipv4', {'aws-token': ''}),  # AWS
    ('http://169.254.169.254/computeMetadata/v1/instance/network-interfaces/0/access-configs/0/external-ip', {'Metadata-Flavor': 'Google'}),
    ('http://169.254.169.254/metadata/instance/network/interface/0/ipv4/ipAddress/0/publicIpAddress?api-version=2021-02-01&format=text', {'Metadata': 'true'}),  # Azure
)


def container_host():
    if Path('/.dockerenv').exists() or Path('/run/.containerenv').exists():
        return True
    try:
        return any(marker in Path('/proc/1/cgroup').read_text() for marker in ('/docker/', '/kubepods/', '/lxc/'))
    except OSError:
        return False


def global_ipv4(text):
    try:
        address = ipaddress.ip_address(text.strip())
    except ValueError:
        return ''
    return str(address) if address.version == 4 and address.is_global else ''


def metadata_public_ipv4(url, headers, timeout=1):
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    headers = dict(headers)
    if 'aws-token' in headers:
        del headers['aws-token']
        token = urllib.request.Request('http://169.254.169.254/latest/api/token', method='PUT', headers={'X-aws-ec2-metadata-token-ttl-seconds': '60'})
        with opener.open(token, timeout=timeout) as response:
            headers['X-aws-ec2-metadata-token'] = response.read(256).decode()
    with opener.open(urllib.request.Request(url, headers=headers), timeout=timeout) as response:
        return global_ipv4(response.read(64).decode(errors='replace'))


def detect_public_ipv4(timeout=1.5):
    answers = queue.Queue()

    def probe(url, headers):
        try:
            answers.put(metadata_public_ipv4(url, headers))
        except Exception:
            answers.put('')

    # Daemon threads: probes of other providers' unreachable addresses must not delay exit.
    for url, headers in PUBLIC_IPV4_METADATA:
        threading.Thread(target=probe, args=(url, headers), daemon=True).start()
    deadline = time.monotonic() + timeout
    for _ in PUBLIC_IPV4_METADATA:
        try:
            answer = answers.get(timeout=max(0, deadline - time.monotonic()))
        except queue.Empty:
            break
        if answer:
            return answer
    return ''


def url_for(host, port):
    return f'http://[{host}]:{port}' if ':' in host else f'http://{host}:{port}'


def access_addresses(port, environ=os.environ, public_ip=''):
    """Return ([(label, url)], note) for the addresses users can open."""
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
            return [('Access', public)], 'Loopback binding: the public URL requires a reverse proxy on this server.'
        return [('Access', public)], 'Allow the service port in the cloud security group/firewall, or route it through your HTTPS reverse proxy.'
    if bind in ('127.0.0.1', 'localhost', '::1'):
        return [('This machine', url_for(bind, port))], 'Local access only. Set LOOPFORGE_BIND=0.0.0.0 and LOOPFORGE_PUBLIC_URL in .env for team access.'
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
    public_hosts = list(dict.fromkeys(([public_ip] if public_ip else []) + [host for host in hosts if ipaddress.ip_address(host).is_global]))
    entries = [('Public', url_for(host, port)) for host in public_hosts]
    entries += [('LAN', url_for(host, port)) for host in hosts if host not in public_hosts]
    if bind in ('0.0.0.0', '::', '[::]'):
        entries.append(('This machine', url_for('localhost', port)))
    if public_hosts:
        return entries, f'Allow TCP {port} in the cloud security group/firewall so the public address is reachable.'
    return entries, f'No public IP detected. On a cloud host, open http://<public IP>:{port} and allow TCP {port} in the security group/firewall.'


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
    detect = not os.environ.get('LOOPFORGE_PUBLIC_URL') and os.environ.get('LOOPFORGE_BIND', '0.0.0.0') in ('0.0.0.0', '::', '[::]') and not container_host()
    addresses, note = access_addresses(sys.argv[1], public_ip=detect_public_ipv4() if detect else '')
    print('\n✓ LoopForge is running')
    width = max((len(label) for label, _ in addresses), default=0)
    for label, address in addresses:
        print(f'  {label + ":":<{width + 1}} {address}')
    if not addresses:
        print(f'  Listening on {os.environ.get("LOOPFORGE_BIND", "0.0.0.0")}:{sys.argv[1]}; open http://SERVER_IP:{sys.argv[1]}.')
    print(f'  {note}')
