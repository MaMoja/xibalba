// Package clientip works out the real address of the client behind a request.
//
// The address of the TCP connection is the only thing a client cannot forge.
// Forwarding headers such as X-Forwarded-For are plain text that anyone can
// send, so they are believed only when the connection comes from a reverse
// proxy the administrator has listed as trusted. Every later decision that
// depends on an address (rules, crawler verification, block lists) builds on
// the result of this package, so it is deliberately strict.
package clientip

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
)

// maxHops bounds how many X-Forwarded-For entries are examined, so a request
// with an enormous header costs a fixed amount of work.
const maxHops = 32

// Info is what is known about where a request came from.
type Info struct {
	// Client is the address the request is attributed to. It is invalid (the
	// zero Addr) only if the connection's own address could not be read.
	Client netip.Addr
	// Peer is the address of the TCP connection.
	Peer netip.Addr
	// PeerTrusted reports whether Peer is a trusted proxy, which means its
	// forwarding headers were believed and may be passed on.
	PeerTrusted bool
}

// Resolver decides which address a request is attributed to.
// It is immutable after New and safe for concurrent use.
type Resolver struct {
	trusted []netip.Prefix
}

// New returns a Resolver that believes forwarding headers only from peers
// inside the trusted networks. With no networks it believes no header.
func New(trusted []netip.Prefix) *Resolver {
	return &Resolver{trusted: append([]netip.Prefix(nil), trusted...)}
}

// Trusted reports whether addr belongs to a trusted proxy.
func (r *Resolver) Trusted(addr netip.Addr) bool {
	for _, prefix := range r.trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// Resolve attributes a request to a client address.
//
// remoteAddr is the connection's address (http.Request.RemoteAddr) and
// forwardedFor holds the values of all X-Forwarded-For headers.
//
// If the peer is not a trusted proxy, the peer is the client and the header
// is ignored. Otherwise the header is read from right to left, which is from
// the hop nearest to Xibalba outwards: trusted proxies are skipped, and the
// first address that is not a trusted proxy is the client. An entry that is
// not an address ends the walk, so everything to its left, which a client
// could have written, is ignored.
func (r *Resolver) Resolve(remoteAddr string, forwardedFor []string) Info {
	peer, ok := parseAddr(remoteAddr)
	if !ok {
		return Info{}
	}
	info := Info{Client: peer, Peer: peer, PeerTrusted: r.Trusted(peer)}
	if !info.PeerTrusted {
		return info
	}

	hops := 0
	for i := len(forwardedFor) - 1; i >= 0; i-- {
		entries := strings.Split(forwardedFor[i], ",")
		for j := len(entries) - 1; j >= 0; j-- {
			if hops++; hops > maxHops {
				return info
			}
			addr, ok := parseAddr(entries[j])
			if !ok {
				return info
			}
			info.Client = addr
			if !r.Trusted(addr) {
				return info
			}
		}
	}
	return info
}

// parseAddr reads an address with or without a port, such as "192.0.2.7",
// "192.0.2.7:443", "2001:db8::1" or "[2001:db8::1]:443". IPv4-mapped IPv6
// addresses are returned as IPv4 and zones are dropped, so one client always
// has one representation.
func parseAddr(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if addr, err := netip.ParseAddr(s); err == nil {
		return addr.Unmap().WithZone(""), true
	}
	if addrPort, err := netip.ParseAddrPort(s); err == nil {
		return addrPort.Addr().Unmap().WithZone(""), true
	}
	return netip.Addr{}, false
}

type contextKey struct{}

// NewContext returns ctx carrying info.
func NewContext(ctx context.Context, info Info) context.Context {
	return context.WithValue(ctx, contextKey{}, info)
}

// FromContext returns the Info stored by Middleware. The second result is
// false if the request did not pass through Middleware.
func FromContext(ctx context.Context) (Info, bool) {
	info, ok := ctx.Value(contextKey{}).(Info)
	return info, ok
}

// Middleware resolves the client address of every request once and stores it
// in the request context for the stages that follow.
func Middleware(resolver *Resolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := resolver.Resolve(r.RemoteAddr, r.Header.Values("X-Forwarded-For"))
		next.ServeHTTP(w, r.WithContext(NewContext(r.Context(), info)))
	})
}
