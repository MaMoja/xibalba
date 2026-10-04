package origin

import (
	"fmt"
	"net/netip"
	"reflect"
	"sync"
	"testing"
)

func TestNetwork(t *testing.T) {
	tests := []struct{ addr, want string }{
		{"192.0.2.77", "192.0.2.0/24"},
		{"::ffff:192.0.2.77", "192.0.2.0/24"},
		{"2001:db8:1:2:3:4:5:6", "2001:db8:1::/48"},
		{"fe80::1%eth0", "fe80::/48"},
	}
	for _, tt := range tests {
		got, ok := Network(netip.MustParseAddr(tt.addr))
		if !ok || got.String() != tt.want {
			t.Errorf("Network(%s) = %s, %v; want %s", tt.addr, got, ok, tt.want)
		}
	}
	if _, ok := Network(netip.Addr{}); ok {
		t.Error("an invalid address has a network")
	}
}

func TestCountAndDrain(t *testing.T) {
	c := New(2)
	c.Note(netip.MustParseAddr("192.0.2.1"), "allow")
	c.Note(netip.MustParseAddr("192.0.2.200"), "allow")
	c.Note(netip.MustParseAddr("192.0.2.1"), "deny")
	c.Note(netip.MustParseAddr("2001:db8::1"), "challenge")
	c.Note(netip.MustParseAddr("198.51.100.1"), "deny")  // table is full
	c.Note(netip.MustParseAddr("198.51.100.1"), "weigh") // not an outcome
	c.Note(netip.Addr{}, "allow")
	want := map[string]uint64{
		"network|192.0.2.0/24|allow":      2,
		"network|192.0.2.0/24|deny":       1,
		"network|2001:db8::/48|challenge": 1,
		"network|other|deny":              1,
	}
	if got := c.Drain(); !reflect.DeepEqual(got, want) {
		t.Errorf("Drain = %v\nwant    %v", got, want)
	}
	if got := c.Drain(); len(got) != 0 {
		t.Errorf("second Drain = %v", got)
	}
	// After a drain there is room again.
	c.Note(netip.MustParseAddr("198.51.100.1"), "deny")
	if got := c.Drain(); got["network|198.51.100.0/24|deny"] != 1 {
		t.Errorf("after drain = %v", got)
	}
}

// No name may hold a single address.
func TestNoSingleAddressInNames(t *testing.T) {
	c := New(10)
	c.Note(netip.MustParseAddr("192.0.2.77"), "allow")
	c.Note(netip.MustParseAddr("2001:db8:1:2:3:4:5:6"), "allow")
	for name := range c.Drain() {
		network, _, _ := split(name)
		p, err := netip.ParsePrefix(network)
		if err != nil || p.Bits() > 48 || (p.Addr().Is4() && p.Bits() > 24) || p != p.Masked() {
			t.Errorf("name %q is finer than a network", name)
		}
	}
}

func TestTop(t *testing.T) {
	counts := map[string]uint64{
		"network|192.0.2.0/24|allow":    5,
		"network|192.0.2.0/24|deny":     5,
		"network|198.51.100.0/24|allow": 7,
		"network|203.0.113.0/24|deny":   3,
		"network|2001:db8::/48|deny":    1,
		"network|other|deny":            2,
		"decision|rule:x|deny":          99,
	}
	Top(counts, 2)
	want := map[string]uint64{
		"network|192.0.2.0/24|allow":    5,
		"network|192.0.2.0/24|deny":     5,
		"network|198.51.100.0/24|allow": 7,
		"network|other|deny":            6,
		"decision|rule:x|deny":          99,
	}
	if !reflect.DeepEqual(counts, want) {
		t.Errorf("Top = %v\nwant  %v", counts, want)
	}
	Top(counts, 0)
	if len(counts) != 3 || counts["network|other|allow"] != 12 || counts["network|other|deny"] != 11 {
		t.Errorf("Top(0) = %v", counts)
	}
}

func TestConcurrentUse(t *testing.T) {
	c := New(50)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				c.Note(netip.MustParseAddr(fmt.Sprintf("10.%d.%d.1", g, i%100)), "allow")
				if i%100 == 0 {
					c.Drain()
				}
			}
		}(g)
	}
	wg.Wait()
}

func BenchmarkNote(b *testing.B) {
	c := New(1000)
	addr := netip.MustParseAddr("192.0.2.1")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		c.Note(addr, "allow")
	}
}
