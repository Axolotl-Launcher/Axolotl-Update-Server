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
