// Package origin counts requests per network of origin, in a bounded table.
//
// A network is an IPv4 /24 or an IPv6 /48: large enough that it is not one
// person's connection, small enough to tell an operator where load comes
// from. No single address is kept. The counts are handed over with Drain
// and forgotten; internal/stats writes them down by the hour.
package origin

import (
	"net/netip"
	"sort"
	"strings"
	"sync"
)

// Prefix of the names Drain returns: "network|<network>|<action>".
const Prefix = "network|"

// Other is the network name for everything beyond the table or the top.
const Other = "other"

// MaxNetworks is how many networks a Counter in the program tracks between
// two drains (one minute). It bounds memory under a flood from very many
// networks; what does not fit is counted under Other.
const MaxNetworks = 10000

// Actions are the outcomes counted per network.
var Actions = [...]string{"allow", "challenge", "deny"}

// Counter counts requests per network. It is safe for use from many
// goroutines. The zero value is not usable; use New.
type Counter struct {
	max int

	mu       sync.Mutex
	networks map[netip.Prefix]*[len(Actions)]uint64
	other    [len(Actions)]uint64
}

// New returns a Counter that tracks at most max networks between two calls
// of Drain. Requests from further networks are counted under Other.
func New(max int) *Counter {
	if max < 1 {
		max = 1
	}
	return &Counter{max: max, networks: map[netip.Prefix]*[len(Actions)]uint64{}}
}

// Network returns the network addr is counted under.
func Network(addr netip.Addr) (netip.Prefix, bool) {
	addr = addr.Unmap().WithZone("")
	if !addr.IsValid() {
		return netip.Prefix{}, false
	}
	bits := 48
	if addr.Is4() {
		bits = 24
	}
	p, err := addr.Prefix(bits)
	return p, err == nil
}

// Note counts one request from addr with the given outcome. An invalid
// address or an unknown action is not counted.
func (c *Counter) Note(addr netip.Addr, action string) {
	index := -1
	for i, a := range Actions {
		if a == action {
			index = i
		}
	}
	network, ok := Network(addr)
	if index < 0 || !ok {
		return
	}
	c.mu.Lock()
	counts := c.networks[network]
	switch {
	case counts != nil:
		counts[index]++
	case len(c.networks) < c.max:
		counts = new([len(Actions)]uint64)
		counts[index] = 1
		c.networks[network] = counts
	default:
		c.other[index]++
	}
	c.mu.Unlock()
}

// Drain returns what was counted since the last call, by name
// ("network|192.0.2.0/24|deny"), and forgets it.
func (c *Counter) Drain() map[string]uint64 {
	c.mu.Lock()
	networks, other := c.networks, c.other
	c.networks, c.other = make(map[netip.Prefix]*[len(Actions)]uint64, len(networks)), [len(Actions)]uint64{}
	c.mu.Unlock()

	out := make(map[string]uint64, len(networks))
	for network, counts := range networks {
		for i, n := range counts {
			if n > 0 {
				out[Prefix+network.String()+"|"+Actions[i]] = n
			}
		}
	}
	for i, n := range other {
		if n > 0 {
			out[Prefix+Other+"|"+Actions[i]] = n
		}
	}
	return out
}

// Top keeps the n networks with the most requests in counts and adds all
// others to Other, in place. Names that are not network counts are left
// alone. It is what an hour is reduced to before it is written down.
func Top(counts map[string]uint64, n int) {
	totals := map[string]uint64{}
	for name, count := range counts {
		if network, _, ok := split(name); ok && network != Other {
			totals[network] += count
		}
	}
	if len(totals) <= n {
		return
	}
	ranked := make([]string, 0, len(totals))
	for network := range totals {
		ranked = append(ranked, network)
	}
	sort.Slice(ranked, func(a, b int) bool {
		if totals[ranked[a]] != totals[ranked[b]] {
			return totals[ranked[a]] > totals[ranked[b]]
		}
		return ranked[a] < ranked[b]
	})
	keep := map[string]bool{}
	for _, network := range ranked[:max(n, 0)] {
		keep[network] = true
	}
	for name, count := range counts {
		if network, action, ok := split(name); ok && network != Other && !keep[network] {
			delete(counts, name)
			counts[Prefix+Other+"|"+action] += count
		}
	}
}

// split takes "network|<network>|<action>" apart.
func split(name string) (network, action string, ok bool) {
	rest, found := strings.CutPrefix(name, Prefix)
	if !found {
		return "", "", false
	}
	i := strings.LastIndexByte(rest, '|')
	if i < 0 {
		return "", "", false
	}
	return rest[:i], rest[i+1:], true
}
