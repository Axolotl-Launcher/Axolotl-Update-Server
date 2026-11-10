package httpapi

import (
	"net"
	"net/netip"
	"strings"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/config"
)

type IPResolver struct{ trusted, cloudflare []netip.Prefix }

func NewIPResolver(cidrs []string) *IPResolver {
	r := &IPResolver{}
	for _, v := range cidrs {
		if p, e := netip.ParsePrefix(v); e == nil {
			r.trusted = append(r.trusted, p)
		}
	}
	for _, v := range config.CloudflareCIDRs {
		r.cloudflare = append(r.cloudflare, netip.MustParsePrefix(v))
	}
	return r
}
func contains(ps []netip.Prefix, a netip.Addr) bool {
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
func (r *IPResolver) TrustedPeer(remote string) bool {
	host, _, e := net.SplitHostPort(remote)
	if e != nil {
		host = remote
	}
	ip, e := netip.ParseAddr(host)
	return e == nil && contains(r.trusted, ip.Unmap())
}

// ViaCloudflare stops at the first non-local-proxy hop, before resolving the
// visitor IP. CF-Connecting-IP/CF-Ray are not proof of a Cloudflare connection.
func (r *IPResolver) ViaCloudflare(remote, forwarded string) bool {
	host, _, e := net.SplitHostPort(remote)
	if e != nil {
		host = remote
	}
	peer, e := netip.ParseAddr(host)
	if e != nil {
		return false
	}
	peer = peer.Unmap()
	if contains(r.cloudflare, peer) {
		return true
	}
	if !contains(r.trusted, peer) || forwarded == "" {
		return false
	}
	values := strings.Split(forwarded, ",")
	if len(values) > 64 {
		return false
	}
	for i := len(values) - 1; i >= 0; i-- {
		next, e := netip.ParseAddr(strings.TrimSpace(values[i]))
		if e != nil {
			return false
		}
		next = next.Unmap()
		if contains(r.cloudflare, next) {
			return true
		}
		if !contains(r.trusted, next) {
			return false
		}
	}
	return false
}
func (r *IPResolver) Resolve(remote, forwarded, cf string) string {
	host, _, e := net.SplitHostPort(remote)
	if e != nil {
		host = remote
	}
	peer, e := netip.ParseAddr(host)
	if e != nil {
		return ""
	}
	peer = peer.Unmap()
	if !contains(r.trusted, peer) {
		return peer.String()
	}
	values := []string{}
	if forwarded != "" {
		values = strings.Split(forwarded, ",")
	}
	if len(values) > 64 {
		return peer.String()
	}
	addresses := []netip.Addr{}
	for _, value := range values {
		a, e := netip.ParseAddr(strings.TrimSpace(value))
		if e != nil {
			return peer.String()
		}
		addresses = append(addresses, a.Unmap())
	}
	client := peer
	cloudflareHop := false
	for i := len(addresses) - 1; i >= 0; i-- {
		if !contains(r.trusted, client) {
			break
		}
		cloudflareHop = cloudflareHop || contains(r.cloudflare, client)
		client = addresses[i]
	}
	cloudflareHop = cloudflareHop || (contains(r.trusted, client) && contains(r.cloudflare, client))
	if cloudflareHop && cf != "" {
		if a, e := netip.ParseAddr(strings.TrimSpace(cf)); e == nil {
			return a.Unmap().String()
		}
	}
	return client.String()
}
