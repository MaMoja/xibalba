package clientip

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func prefixes(list ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(list))
	for _, s := range list {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

func TestResolve(t *testing.T) {
	trusted := prefixes("10.0.0.0/8", "127.0.0.1/32", "2001:db8:ffff::/48")

	tests := []struct {
		name        string
		trusted     []netip.Prefix
		remote      string
		xff         []string
		wantClient  string
		wantTrusted bool
	}{
		// Nothing trusted: the header is never believed.
		{"no trust, no header", nil, "203.0.113.9:4000", nil, "203.0.113.9", false},
		{"no trust, spoofed header ignored", nil, "203.0.113.9:4000", []string{"1.2.3.4"}, "203.0.113.9", false},
		{"no trust, spoofed loopback ignored", nil, "203.0.113.9:4000", []string{"127.0.0.1"}, "203.0.113.9", false},

		// Untrusted peer while proxies are configured: still not believed.
		{"untrusted peer, header ignored", trusted, "203.0.113.9:4000", []string{"10.0.0.1"}, "203.0.113.9", false},

		// Trusted peer.
		{"trusted peer, no header", trusted, "10.0.0.5:4000", nil, "10.0.0.5", true},
		{"trusted peer, one hop", trusted, "10.0.0.5:4000", []string{"198.51.100.7"}, "198.51.100.7", true},
		{"trusted peer, chain of trusted proxies", trusted, "10.0.0.5:4000",
			[]string{"198.51.100.7, 10.0.0.9, 10.0.0.8"}, "198.51.100.7", true},
		{"client-supplied prefix is not believed", trusted, "10.0.0.5:4000",
			[]string{"1.2.3.4, 198.51.100.7"}, "198.51.100.7", true},
		{"spoofed trusted address left of the real client", trusted, "10.0.0.5:4000",
			[]string{"10.0.0.1, 198.51.100.7"}, "198.51.100.7", true},
		{"several header lines are read in order", trusted, "10.0.0.5:4000",
			[]string{"1.2.3.4", "198.51.100.7, 10.0.0.8"}, "198.51.100.7", true},
		{"all hops trusted: leftmost wins", trusted, "10.0.0.5:4000",
			[]string{"10.0.0.7, 10.0.0.8"}, "10.0.0.7", true},

		// Garbage ends the walk; nothing to its left is believed.
		{"garbage entry stops at the peer", trusted, "10.0.0.5:4000",
			[]string{"198.51.100.7, unknown"}, "10.0.0.5", true},
		{"garbage left of a trusted hop", trusted, "10.0.0.5:4000",
			[]string{"198.51.100.7, <script>, 10.0.0.8"}, "10.0.0.8", true},
		{"empty entry", trusted, "10.0.0.5:4000", []string{"198.51.100.7,"}, "10.0.0.5", true},

		// Address forms.
		{"entry with port", trusted, "10.0.0.5:4000", []string{"198.51.100.7:55123"}, "198.51.100.7", true},
		{"ipv6 entry", trusted, "10.0.0.5:4000", []string{"2001:db8::1"}, "2001:db8::1", true},
		{"bracketed ipv6 with port", trusted, "10.0.0.5:4000", []string{"[2001:db8::1]:443"}, "2001:db8::1", true},
		{"ipv6 peer", trusted, "[2001:db8:ffff::2]:4000", []string{"198.51.100.7"}, "198.51.100.7", true},
		{"ipv4-mapped peer is matched as ipv4", trusted, "[::ffff:10.0.0.5]:4000", []string{"198.51.100.7"}, "198.51.100.7", true},
		{"ipv4-mapped entry cannot dodge the trusted list", trusted, "10.0.0.5:4000",
			[]string{"198.51.100.7, ::ffff:10.0.0.8"}, "198.51.100.7", true},
		{"zone is dropped", nil, "[fe80::1%eth0]:4000", nil, "fe80::1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := New(tt.trusted).Resolve(tt.remote, tt.xff)
			if got := info.Client.String(); got != tt.wantClient {
				t.Errorf("client = %s, want %s", got, tt.wantClient)
			}
			if info.PeerTrusted != tt.wantTrusted {
				t.Errorf("peer trusted = %v, want %v", info.PeerTrusted, tt.wantTrusted)
			}
		})
	}
}

func TestResolveUnreadablePeer(t *testing.T) {
	for _, remote := range []string{"", "not-an-address", "@"} {
		info := New(prefixes("0.0.0.0/0")).Resolve(remote, []string{"198.51.100.7"})
		if info.Client.IsValid() || info.PeerTrusted {
			t.Errorf("Resolve(%q) = %+v, want an invalid client that is not trusted", remote, info)
		}
	}
}

func TestResolveBoundsWorkOnHugeHeaders(t *testing.T) {
	// 10,000 trusted hops with the "client" at the far left: the walk must
	// give up after maxHops instead of reading them all.
	header := "198.51.100.7" + strings.Repeat(", 10.0.0.8", 10000)
	info := New(prefixes("10.0.0.0/8")).Resolve("10.0.0.5:4000", []string{header})
	if got := info.Client.String(); got != "10.0.0.8" {
		t.Errorf("client = %s, want the walk to stop at a trusted hop (10.0.0.8)", got)
	}
}

func TestNewCopiesItsInput(t *testing.T) {
	list := prefixes("10.0.0.0/8")
	r := New(list)
	list[0] = netip.MustParsePrefix("0.0.0.0/0") // caller changes its slice afterwards
	if r.Trusted(netip.MustParseAddr("203.0.113.9")) {
		t.Error("the resolver's trusted list changed when the caller's slice did")
	}
}

func TestMiddlewareStoresInfo(t *testing.T) {
	var got Info
	var found bool
	h := Middleware(New(prefixes("192.0.2.1/32")), http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, found = FromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	req.Header.Add("X-Forwarded-For", "198.51.100.7")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !found || got.Client.String() != "198.51.100.7" || got.Peer.String() != "192.0.2.1" || !got.PeerTrusted {
		t.Errorf("info = %+v (found %v)", got, found)
	}
}

func TestFromContextWithoutMiddleware(t *testing.T) {
	if _, ok := FromContext(httptest.NewRequest(http.MethodGet, "/", nil).Context()); ok {
		t.Error("FromContext reported info for a request that never passed the middleware")
	}
}
