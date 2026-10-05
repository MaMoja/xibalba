package rules

import (
	"math/rand"
	"net/netip"
	"strings"
	"testing"
)

func prefixes(list ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(list))
	for i, s := range list {
		p, err := parsePrefix(s)
		if err != nil {
			panic(err)
		}
		out[i] = p
	}
	return out
}

func TestAddressSet(t *testing.T) {
	set := NewAddressSet(prefixes("192.0.2.0/24", "192.0.3.0/24", "198.51.100.7", "10.0.0.0/8", "10.1.0.0/16",
		"2001:db8::/32", "2001:db8:1::/48", "2a00::1", "255.255.255.255", "0.0.0.0/32"))
	in := []string{"192.0.2.0", "192.0.2.255", "192.0.3.9", "198.51.100.7", "10.255.255.255", "10.1.2.3", "2001:db8::1",
		"2001:db8:ffff:ffff:ffff:ffff:ffff:ffff", "2a00::1", "255.255.255.255", "0.0.0.0", "::ffff:192.0.2.9"}
	out := []string{"192.0.1.255", "192.0.4.0", "198.51.100.6", "198.51.100.8", "11.0.0.0", "9.255.255.255", "2001:db9::", "2001:db7:ffff::",
		"2a00::2", "2a00::", "255.255.255.254", "0.0.0.1", "::1"}
	for _, a := range in {
		if !set.Contains(netip.MustParseAddr(a)) {
			t.Errorf("%s is not in the set", a)
		}
	}
	for _, a := range out {
		if set.Contains(netip.MustParseAddr(a)) {
			t.Errorf("%s is in the set", a)
		}
	}
	if set.Contains(netip.Addr{}) || (*AddressSet)(nil).Contains(netip.MustParseAddr("192.0.2.1")) {
		t.Error("an invalid address or a missing set matched")
	}
	if len(set.v4) != 5 { // 192.0.2.0/23 merged, 10/8 swallowed 10.1/16
		t.Errorf("%d IPv4 spans, want 5: %+v", len(set.v4), set.v4)
	}
	if NewAddressSet(prefixes("0.0.0.0/0")).Contains(netip.MustParseAddr("::1")) || !NewAddressSet(prefixes("0.0.0.0/0", "::/0")).Contains(netip.MustParseAddr("::1")) {
		t.Error("the whole of IPv4 and IPv6 are mixed up")
	}
	if NewAddressSet(nil).Contains(netip.MustParseAddr("192.0.2.1")) {
		t.Error("an empty set matched")
	}
}

// The set must agree with testing every network in turn.
func TestAddressSetAgreesWithPlainSearch(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	var list []netip.Prefix
	for i := 0; i < 3000; i++ {
		var p netip.Prefix
		if i%3 == 0 {
			var a [16]byte
			a[0], a[1], a[2], a[3], a[4], a[5] = 0x20, 0x01, byte(rng.Intn(4)), byte(rng.Intn(256)), byte(rng.Intn(256)), byte(rng.Intn(256))
			p = netip.PrefixFrom(netip.AddrFrom16(a), 24+rng.Intn(40)).Masked()
		} else {
			a := [4]byte{byte(10 + rng.Intn(3)), byte(rng.Intn(256)), byte(rng.Intn(256)), byte(rng.Intn(256))}
			p = netip.PrefixFrom(netip.AddrFrom4(a), 12+rng.Intn(21)).Masked()
		}
		list = append(list, p)
	}
	set := NewAddressSet(list)
	for i := 0; i < 20000; i++ {
		var addr netip.Addr
		if i%3 == 0 {
			var a [16]byte
			a[0], a[1], a[2], a[3], a[4], a[5], a[15] = 0x20, 0x01, byte(rng.Intn(5)), byte(rng.Intn(256)), byte(rng.Intn(256)), byte(rng.Intn(256)), byte(rng.Intn(256))
			addr = netip.AddrFrom16(a)
		} else {
			addr = netip.AddrFrom4([4]byte{byte(9 + rng.Intn(5)), byte(rng.Intn(256)), byte(rng.Intn(256)), byte(rng.Intn(256))})
		}
		want := false
		for _, p := range list {
			if p.Contains(addr) {
				want = true
				break
			}
		}
		if got := set.Contains(addr); got != want {
			t.Fatalf("%s: set says %v, the networks say %v", addr, got, want)
		}
	}
}

func TestReadAddressList(t *testing.T) {
	set, problems := ReadAddressList(strings.NewReader("\xef\xbb\xbf# VPN exits\n192.0.2.0/24\n\n  198.51.100.7   # one host\n2001:db8::/32;comment\n203.0.113.0/24,AS64500,Example\r\n10.0.0.1\tnote\n"))
	if len(problems) != 0 || set.Len() != 5 {
		t.Fatalf("problems %+v, %d entries", problems, set.Len())
	}
	for _, a := range []string{"192.0.2.9", "198.51.100.7", "2001:db8::9", "203.0.113.200", "10.0.0.1"} {
		if !set.Contains(netip.MustParseAddr(a)) {
			t.Errorf("%s missing", a)
		}
	}
	if set, problems := ReadAddressList(strings.NewReader("")); len(problems) != 0 || set.Len() != 0 {
		t.Error("an empty list is not a list without entries")
	}

	bad := "192.0.2.0/24\nnot an address\n300.1.1.1\n192.0.2.0/33\nexample.org\n" + strings.Repeat("x", 600) + "\n"
	set, problems = ReadAddressList(strings.NewReader(bad))
	if set != nil || len(problems) != 5 || problems[0].Line != 2 || problems[3].Line != 5 {
		t.Errorf("set %v, problems %+v", set, problems)
	}
	if len(problems[4].Message) > 200 {
		t.Errorf("a long line is repeated in the message: %d characters", len(problems[4].Message))
	}
	var many strings.Builder
	for i := 0; i < 50; i++ {
		many.WriteString("nonsense\n")
	}
	if _, problems := ReadAddressList(strings.NewReader(many.String())); len(problems) != 10 {
		t.Errorf("%d problems reported, want 10 at most", len(problems))
	}
}

func TestASNAndAddressListConditions(t *testing.T) {
	lists := map[string]*AddressSet{"vpn": NewAddressSet(prefixes("192.0.2.0/24")), "hosting": NewAddressSet(prefixes("2001:db8::/32"))}
	e, problems := Compile(Spec{DefaultAction: Allow, ASN: true, AddressLists: lists, Rules: []RuleSpec{
		{Name: "vpn", Match: MatchSpec{AddressList: []string{"vpn", "hosting"}}, Action: Challenge},
		{Name: "operators", Match: MatchSpec{ASN: []uint32{64501, 64500, 4200000000}}, Action: Deny},
		{Name: "not-ours", Match: MatchSpec{Not: &MatchSpec{ASN: []uint32{64510}}, Path: &StringSpec{Prefix: "/intern"}}, Action: Deny},
	}})
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	if !e.UsesASN() {
		t.Error("UsesASN is false")
	}
	cases := []struct {
		client, path string
		asn          uint32
		noData       bool
		want         string
	}{
		{"192.0.2.7", "/", 0, false, "rule:vpn"},
		{"2001:db8::1", "/", 64500, false, "rule:vpn"},
		{"203.0.113.1", "/", 64500, false, "rule:operators"},
		{"203.0.113.1", "/", 4200000000, false, "rule:operators"},
		{"203.0.113.1", "/", 64502, false, "default"},
		{"203.0.113.1", "/", 0, false, "default"},
		{"203.0.113.1", "/intern", 64510, false, "default"},
		{"203.0.113.1", "/intern", 64999, false, "rule:not-ours"},
		{"203.0.113.1", "/intern", 0, false, "rule:not-ours"},
		// Without a database nobody is "not ours": the rule is skipped.
		{"203.0.113.1", "/intern", 0, true, "default"},
		{"203.0.113.1", "/", 64500, true, "default"},
		{"192.0.2.7", "/", 0, true, "rule:vpn"},
	}
	for _, c := range cases {
		req := Request{Method: "GET", Path: c.path, Client: netip.MustParseAddr(c.client), ASN: c.asn, NoASNData: c.noData}
		d := e.Evaluate(&req)
		if got := e.Sources()[d.Source].ID; got != c.want {
			t.Errorf("%+v: decided by %s, want %s", c, got, c.want)
		}
	}
	req := Request{Method: "GET", Path: "/", Client: netip.MustParseAddr("203.0.113.1"), ASN: 64502}
	if n := testing.AllocsPerRun(100, func() {
		e.Evaluate(&req)
	}); n != 0 {
		t.Errorf("%v allocations per request", n)
	}
}

func TestASNAndAddressListMistakes(t *testing.T) {
	lists := map[string]*AddressSet{"vpn": NewAddressSet(nil)}
	cases := []struct {
		name  string
		spec  Spec
		field string
		text  string
	}{
		{"no database", Spec{Rules: []RuleSpec{{Name: "r", Match: MatchSpec{ASN: []uint32{64500}}, Action: Deny}}}, "match.asn", "no database of network operators"},
		{"empty", Spec{ASN: true, Rules: []RuleSpec{{Name: "r", Match: MatchSpec{ASN: []uint32{}}, Action: Deny}}}, "match.asn", "0 entries"},
		{"zero", Spec{ASN: true, Rules: []RuleSpec{{Name: "r", Match: MatchSpec{ASN: []uint32{0, 5}}, Action: Deny}}}, "match.asn", "0 is not"},
		{"too many", Spec{ASN: true, Rules: []RuleSpec{{Name: "r", Match: MatchSpec{ASN: make([]uint32, MaxASNs+1)}, Action: Deny}}}, "match.asn", "entries"},
		{"unknown list", Spec{AddressLists: lists, Rules: []RuleSpec{{Name: "r", Match: MatchSpec{AddressList: []string{"tor"}}, Action: Deny}}}, "match.address_list[0]", "the lists are: vpn"},
		{"no lists", Spec{Rules: []RuleSpec{{Name: "r", Match: MatchSpec{AddressList: []string{"tor"}}, Action: Deny}}}, "match.address_list[0]", "rules.address_lists"},
		{"empty list of lists", Spec{AddressLists: lists, Rules: []RuleSpec{{Name: "r", Match: MatchSpec{AddressList: []string{}}, Action: Deny}}}, "match.address_list", "0 entries"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.spec.DefaultAction = Allow
			_, problems := Compile(c.spec)
			found := false
			for _, p := range problems {
				if strings.HasSuffix(p.Field, c.field) && strings.Contains(p.Message+p.Hint, c.text) {
					found = true
				}
			}
			if !found {
				t.Errorf("problems: %+v", problems)
			}
		})
	}
	// An address list is something the client cannot choose; an operator is as broad as a country.
	_, problems := Compile(Spec{DefaultAction: Allow, AddressLists: lists, ASN: true, Rules: []RuleSpec{
		{Name: "office", Match: MatchSpec{AddressList: []string{"vpn"}}, Action: Allow, ExemptFromLimits: true},
	}})
	if len(problems) != 0 {
		t.Errorf("exempt by address list refused: %+v", problems)
	}
	_, problems = Compile(Spec{DefaultAction: Allow, ASN: true, Rules: []RuleSpec{
		{Name: "provider", Match: MatchSpec{ASN: []uint32{64500}}, Action: Allow, ExemptFromLimits: true},
	}})
	if len(problems) == 0 {
		t.Error("a whole network operator may be exempt from the limits")
	}
}

func BenchmarkAddressSet(b *testing.B) {
	rng := rand.New(rand.NewSource(2))
	var list []netip.Prefix
	for i := 0; i < 500000; i++ {
		list = append(list, netip.PrefixFrom(netip.AddrFrom4([4]byte{byte(rng.Intn(224)), byte(rng.Intn(256)), byte(rng.Intn(256)), 0}), 24))
	}
	set := NewAddressSet(list)
	addrs := make([]netip.Addr, 1024)
	for i := range addrs {
		addrs[i] = netip.AddrFrom4([4]byte{byte(rng.Intn(224)), byte(rng.Intn(256)), byte(rng.Intn(256)), byte(rng.Intn(256))})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		set.Contains(addrs[i&1023])
	}
}
