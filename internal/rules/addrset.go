package rules

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/netip"
	"sort"
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
// networks are merged.
func NewAddressSet(networks []netip.Prefix) *AddressSet {
	set := &AddressSet{n: len(networks)}
	for _, p := range networks {
		p = p.Masked()
		addr := p.Addr()
		if addr.Is4() {
			a := addr.As4()
			first := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
			last := first | (1<<(32-p.Bits()) - 1)
			if p.Bits() == 0 {
				last = 1<<32 - 1
			}
			set.v4 = append(set.v4, span4{first, last})
			continue
		}
		first := addr.As16()
		last := first
		for bit := p.Bits(); bit < 128; bit++ {
			last[bit/8] |= 1 << (7 - bit%8)
		}
		set.v6 = append(set.v6, span6{first, last})
	}

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
	set.v4 = merged4

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
	set.v6 = merged6
	return set
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
// (many published lists are tables); only the first field is read. Up to
// ten problems are reported; a list with problems gives no set.
func ReadAddressList(r io.Reader) (*AddressSet, []ListProblem) {
	var networks []netip.Prefix
	var problems []ListProblem
	add := func(line int, message string) {
		if len(problems) < 10 {
			problems = append(problems, ListProblem{Line: line, Message: message})
		}
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 4096), maxAddressListLine)
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		if line == 1 {
			text = strings.TrimPrefix(text, "\xef\xbb\xbf") // a byte-order mark from an editor
		}
		if i := strings.IndexAny(text, "#;"); i >= 0 {
			text = text[:i]
		}
		text = strings.TrimSpace(text)
		if i := strings.IndexAny(text, " \t,"); i >= 0 {
			text = text[:i]
		}
		if text == "" {
			continue
		}
		prefix, err := parsePrefix(text)
		if err != nil {
			add(line, fmt.Sprintf("%q is not an IP address or network", clip(text)))
			continue
		}
		if len(networks) >= MaxAddressListEntries {
			add(line, fmt.Sprintf("the list has more than %d entries", MaxAddressListEntries))
			break
		}
		networks = append(networks, prefix)
	}
	if err := scanner.Err(); err != nil {
		add(line+1, fmt.Sprintf("the line is longer than %d characters, or the file cannot be read", maxAddressListLine))
	}
	if len(problems) > 0 {
		return nil, problems
	}
	return NewAddressSet(networks), nil
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
