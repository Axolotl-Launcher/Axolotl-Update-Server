from ipaddress import ip_address, ip_network

# Official lists: https://www.cloudflare.com/ips-v4/ and /ips-v6/.
# Kept local so request handling does not depend on an external lookup.
CLOUDFLARE_CIDRS = (
    "173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
    "141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
    "197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
    "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22", "2400:cb00::/32",
    "2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32",
    "2a06:98c0::/29", "2c0f:f248::/32",
)
DEFAULT_TRUSTED_PROXY_CIDRS = ("127.0.0.1/32", "::1/128", *CLOUDFLARE_CIDRS)


def _address(value):
    address = ip_address(value)
    # Normalize IPv4-mapped IPv6 socket addresses before trust checks.
    return getattr(address, "ipv4_mapped", None) or address


class ClientIPMiddleware:
    """Resolve REMOTE_ADDR only through explicitly trusted proxy networks."""

    def __init__(self, app, trusted_cidrs):
        self.app = app
        self.trusted = tuple(ip_network(cidr.strip()) for cidr in trusted_cidrs if cidr.strip())
        self.cloudflare = tuple(ip_network(cidr) for cidr in CLOUDFLARE_CIDRS)

    def resolve(self, environ):
        original = environ.get("REMOTE_ADDR", "")
        try:
            peer = _address(original)
        except ValueError:
            return original
        if not any(peer in network for network in self.trusted):
            return str(peer)

        forwarded = environ.get("HTTP_X_FORWARDED_FOR", "")
        values = forwarded.split(",") if forwarded else []
        if len(values) > 64:
            return str(peer)
        try:
            addresses = [_address(value.strip()) for value in values]
        except ValueError:
            return str(peer)
        client = peer
        cloudflare_hop = False
        # Stop at the first untrusted address. Earlier entries may be supplied
        # by the client and must not be treated as authoritative.
        for address in reversed(addresses):
            if not any(client in network for network in self.trusted):
                break
            cloudflare_hop |= any(client in network for network in self.cloudflare)
            client = address
        cloudflare_hop |= (
            any(client in network for network in self.trusted)
            and any(client in network for network in self.cloudflare)
        )
        if cloudflare_hop and environ.get("HTTP_CF_CONNECTING_IP"):
            try:
                return str(_address(environ["HTTP_CF_CONNECTING_IP"].strip()))
            except ValueError:
                pass
        return str(client)

    def __call__(self, environ, start_response):
        environ["axolotl.proxy_peer"] = environ.get("REMOTE_ADDR", "")
        environ["REMOTE_ADDR"] = self.resolve(environ)
        return self.app(environ, start_response)
