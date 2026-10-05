package rules

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Limits on address lists.
const (
	// MaxAddressListEntries is how many entries one address list may have.
	MaxAddressListEntries = 2_000_000
	maxAddressListLine    = 512
)

// AddressSet is a set of addresses and networks, built once and then only
// asked. Asking costs a search in a sorted list, whatever the size: a set
// of a million networks answers as fast as one of a hundred.
type AddressSet struct {
	v4 []span4 // sorted by first address, not overlapping
	v6 []span6
	n  int // entries the set was built from
}

type span4 struct{ first, last uint32 }

type span6 struct{ first, last [16]byte }

// NewAddressSet builds a set from networks. Overlapping and adjacent
// networks are merged. Networks that are not valid are left out.
func NewAddressSet(networks []netip.Prefix) *AddressSet {
	set := &AddressSet{}
	for _, p := range networks {
		set.add(p)
	}
	set.finish()
	return set
}

// add puts one network into a set that is being built.
func (set *AddressSet) add(p netip.Prefix) {
	if !p.IsValid() {
		return
	}
	if addr := p.Addr(); addr.Is4In6() { // "::ffff:192.0.2.0/120" means 192.0.2.0/24
		if p.Bits() < 96 {
			return
		}
		p = netip.PrefixFrom(addr.Unmap(), p.Bits()-96)
	}
	p = p.Masked()
	set.n++
	addr := p.Addr()
	if addr.Is4() {
		a := addr.As4()
		first := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
		last := uint32(1<<32 - 1)
		if p.Bits() > 0 {
			last = first | (1<<(32-p.Bits()) - 1)
		}
		set.v4 = append(set.v4, span4{first, last})
		return
	}
	first := addr.As16()
	last := first
	for bit := p.Bits(); bit < 128; bit++ {
		last[bit/8] |= 1 << (7 - bit%8)
	}
	set.v6 = append(set.v6, span6{first, last})
}

// finish sorts and merges what was added. After it the set can be asked.
func (set *AddressSet) finish() {
	sort.Slice(set.v4, func(i, j int) bool { return set.v4[i].first < set.v4[j].first })
	merged4 := set.v4[:0]
	for _, s := range set.v4 {
		if n := len(merged4); n > 0 && (s.first <= merged4[n-1].last || s.first == merged4[n-1].last+1) {
			if s.last > merged4[n-1].last {
				merged4[n-1].last = s.last
			}
			continue
		}
		merged4 = append(merged4, s)
	}
	set.v4 = slices.Clip(merged4)

	sort.Slice(set.v6, func(i, j int) bool { return bytes.Compare(set.v6[i].first[:], set.v6[j].first[:]) < 0 })
	merged6 := set.v6[:0]
	for _, s := range set.v6 {
		if n := len(merged6); n > 0 && bytes.Compare(s.first[:], merged6[n-1].last[:]) <= 0 {
			if bytes.Compare(s.last[:], merged6[n-1].last[:]) > 0 {
				merged6[n-1].last = s.last
			}
			continue
		}
		merged6 = append(merged6, s)
	}
	set.v6 = slices.Clip(merged6)
}

// Len returns how many entries the set was built from.
func (s *AddressSet) Len() int {
	if s == nil {
		return 0
	}
	return s.n
}

// Contains reports whether addr is in the set. It does not allocate.
func (s *AddressSet) Contains(addr netip.Addr) bool {
	if s == nil || !addr.IsValid() {
		return false
	}
	addr = addr.Unmap()
	if addr.Is4() {
		a := addr.As4()
		v := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
		// The last span that starts at or before v.
		lo, hi := 0, len(s.v4)
		for lo < hi {
			mid := int(uint(lo+hi) >> 1)
			if s.v4[mid].first <= v {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		return lo > 0 && v <= s.v4[lo-1].last
	}
	v := addr.As16()
	lo, hi := 0, len(s.v6)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if bytes.Compare(s.v6[mid].first[:], v[:]) <= 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo > 0 && bytes.Compare(v[:], s.v6[lo-1].last[:]) <= 0
}

// ListProblem is one line of an address list that cannot be used.
type ListProblem struct {
	Line    int
	Message string
}

// ReadAddressList reads an address list: one address or network per line.
// Empty lines are skipped, and so is everything from a "#" or ";" on. A
// line may hold more after the address, separated by a blank or a comma
// (many published lists are tables); only the first field is read.
//
// A list decides who is checked or refused, so a line is only taken if it
// can mean one thing. Refused are: a second address on the line (a range
// "a - b", or address and mask: write a network), a network with bits set
// beyond its length ("10.0.0.5/8": write 10.0.0.0/8), and a network so
// wide that it is surely a slip (shorter than /8, or /16 for IPv6).
//
// Up to ten problems are reported; a list with problems gives no set.
func ReadAddressList(r io.Reader) (*AddressSet, []ListProblem) {
	set := &AddressSet{}
	var problems []ListProblem
	add := func(line int, message string) {
		if len(problems) < 10 {
			problems = append(problems, ListProblem{Line: line, Message: message})
		}
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, maxAddressListLine), maxAddressListLine)
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Bytes()
		if line == 1 {
			text = bytes.TrimPrefix(text, []byte("\xef\xbb\xbf")) // a byte-order mark from an editor
		}
		if i := bytes.IndexAny(text, "#;"); i >= 0 {
			text = text[:i]
		}
		text = bytes.TrimSpace(text)
		rest := []byte(nil)
		if i := bytes.IndexAny(text, " \t,"); i >= 0 {
			text, rest = text[:i], bytes.TrimLeft(text[i:], " \t,")
		}
		if len(text) == 0 {
			continue
		}
		entry := string(text)
		prefix, message := listEntry(entry)
		if message == "" && len(rest) > 0 {
			// What follows must not be the other half of what was meant.
			second := string(rest)
			if i := strings.IndexAny(second, " \t,"); i >= 0 {
				second = second[:i]
			}
			if _, err := parsePrefix(second); err == nil || strings.HasPrefix(second, "-") {
				message = "holds a second address after " + entry + "; a range or an address with a mask is written as a network, such as 192.0.2.0/24"
			}
		}
		if message != "" {
			add(line, message)
			continue
		}
		if set.n >= MaxAddressListEntries {
			add(line, fmt.Sprintf("the list has more than %d entries", MaxAddressListEntries))
			break
		}
		set.add(prefix)
	}
	switch err := scanner.Err(); {
	case err == bufio.ErrTooLong:
		add(line+1, fmt.Sprintf("the line is longer than %d characters", maxAddressListLine))
	case err != nil:
		add(line+1, "the file cannot be read")
	}
	if len(problems) > 0 {
		return nil, problems
	}
	set.finish()
	return set, nil
}

// listEntry reads one entry of an address list. message says what is wrong
// with it, or is empty.
func listEntry(entry string) (prefix netip.Prefix, message string) {
	// Only text that can be part of an address is repeated in a message:
	// a file named by mistake must not be quoted line by line.
	shown := "the entry"
	if strings.Trim(entry, "0123456789abcdefABCDEF:./") == "" {
		shown = strconv.Quote(clip(entry))
	}
	if strings.Contains(entry, "%") {
		return prefix, "holds an address with a zone (\"%\"), which has no meaning in a list"
	}
	prefix, err := netip.ParsePrefix(entry)
	if err != nil {
		addr, err := netip.ParseAddr(entry)
		if err != nil {
			return prefix, shown + " is not an IP address or network"
		}
		addr = addr.Unmap()
		return netip.PrefixFrom(addr, addr.BitLen()), ""
	}
	if addr := prefix.Addr(); addr.Is4In6() {
		if prefix.Bits() < 96 {
			return prefix, shown + " is an IPv4-mapped network shorter than /96"
		}
		prefix = netip.PrefixFrom(addr.Unmap(), prefix.Bits()-96)
	}
	if prefix.Masked() != prefix {
		return prefix, fmt.Sprintf("%s has bits set beyond its length; if the network is meant, write %s", shown, prefix.Masked())
	}
	if widest := map[bool]int{true: 8, false: 16}[prefix.Addr().Is4()]; prefix.Bits() < widest {
		return prefix, fmt.Sprintf("%s is wider than /%d, a large part of the internet; that is surely a slip", shown, widest)
	}
	return prefix, ""
}

func clip(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

// addressIn tests whether the client address is in one of the sets.
type addressIn []*AddressSet

func (sets addressIn) match(r *Request) bool {
	for _, set := range sets {
		if set.Contains(r.Client) {
			return true
		}
	}
	return false
}

// asnIn tests the network operator the client's address belongs to. The
// numbers are sorted.
type asnIn []uint32

func (list asnIn) match(r *Request) bool {
	if r.ASN == 0 {
		return false
	}
	lo, hi := 0, len(list)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if list[mid] < r.ASN {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo < len(list) && list[lo] == r.ASN
}
