// Package geotest builds small country databases for tests. It writes the
// MaxMind DB file format, version 2, from the published format description.
package geotest

import (
	"encoding/binary"
	"net/netip"
	"sort"
	"strconv"
)

// Options changes how Build writes the file. The zero value gives an IPv6
// database with 24-bit records, laid out as the real country databases are.
type Options struct {
	// RecordSize is 24, 28 or 32. Zero means 24.
	RecordSize int
	// IPv4Only writes a database of IPv4 addresses only.
	IPv4Only bool
	// Layout is how the country is stored: "" or "nested" for
	// country.iso_code, "code" for country_code, "plain" for country.
	// "asn" and "asn-text" write a database of network operators instead:
	// the values of the networks are then numbers ("64500"), stored as
	// autonomous_system_number, or as the text "AS64500" under asn.
	Layout string
	// Pointers stores the country map once and points to it from each record.
	Pointers bool
}

type node struct {
	kids [2]*node
	data int // offset into the data section; -1 for none
	leaf bool
}

// Build returns a database that maps each network to its country code.
func Build(networks map[string]string, opts Options) []byte {
	if opts.RecordSize == 0 {
		opts.RecordSize = 24
	}
	var data []byte
	shared := map[string]int{} // country code -> offset of its shared part
	root := &node{data: -1}

	names := make([]string, 0, len(networks))
	for network := range networks {
		names = append(names, network)
	}
	sort.Strings(names) // a stable file for a given input

	for _, network := range names {
		code := networks[network]
		prefix := netip.MustParsePrefix(network).Masked()
		addr, bits := prefix.Addr(), prefix.Bits()
		raw := addr.As16()
		key := raw[:]
		switch {
		case opts.IPv4Only:
			if !addr.Is4() {
				continue
			}
			key = raw[12:]
		case addr.Is4():
			key = make([]byte, 16) // IPv4 lives in ::/96
			copy(key[12:], raw[12:])
			bits += 96
		}

		offset := len(data)
		switch opts.Layout {
		case "code":
			data = append(data, mapOf(2)...)
			data = append(data, str("continent_code")...)
			data = append(data, str("EU")...)
			data = append(data, str("country_code")...)
			data = append(data, str(code)...)
		case "asn":
			n, _ := strconv.ParseUint(code, 10, 32)
			data = append(data, mapOf(2)...)
			data = append(data, str("autonomous_system_number")...)
			data = append(data, 0xc0|4, byte(n>>24), byte(n>>16), byte(n>>8), byte(n)) // uint32
			data = append(data, str("autonomous_system_organization")...)
			data = append(data, str("Operator "+code)...)
		case "asn-text":
			data = append(data, mapOf(2)...)
			data = append(data, str("name")...)
			data = append(data, str("Operator "+code)...)
			data = append(data, str("asn")...)
			data = append(data, str("AS"+code)...)
		case "plain":
			data = append(data, mapOf(1)...)
			data = append(data, str("country")...)
			data = append(data, str(code)...)
		default:
			country := append(mapOf(3), str("geoname_id")...)
			country = append(country, 0xc0|3, 1, 2, 3) // uint32 in three bytes
			country = append(country, str("iso_code")...)
			country = append(country, str(code)...)
			country = append(country, str("names")...)
			country = append(country, mapOf(2)...)
			country = append(country, str("de")...)
			country = append(country, str("Land "+code)...)
			country = append(country, str("en")...)
			country = append(country, str("Country "+code)...)

			if opts.Pointers {
				at, known := shared[code]
				if !known {
					at = len(data)
					shared[code] = at
					data = append(data, country...)
				}
				offset = len(data)
				country = pointer(at)
			}
			// Other fields come first, as in the real databases, so the
			// reader has to step over a map, an array and a flag.
			data = append(data, mapOf(3)...)
			data = append(data, str("continent")...)
			data = append(data, mapOf(2)...)
			data = append(data, str("code")...)
			data = append(data, str("EU")...)
			data = append(data, str("names")...)
			data = append(data, 2, 4) // array of two, extended type
			data = append(data, str("Europa")...)
			data = append(data, str("Europe")...)
			data = append(data, str("is_test")...)
			data = append(data, 1, 7) // boolean true, extended type
			data = append(data, str("country")...)
			data = append(data, country...)
		}

		n := root
		for i := 0; i < bits; i++ {
			bit := key[i/8] >> (7 - i%8) & 1
			if n.kids[bit] == nil {
				n.kids[bit] = &node{data: -1}
			}
			n = n.kids[bit]
		}
		n.leaf, n.data = true, offset
	}

	// Number the inner nodes, the root first.
	var inner []*node
	number := map[*node]uint32{}
	queue := []*node{root}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if n.leaf {
			continue
		}
		number[n] = uint32(len(inner))
		inner = append(inner, n)
		for _, kid := range n.kids {
			if kid != nil {
				queue = append(queue, kid)
			}
		}
	}
	count := uint32(len(inner))
	value := func(n *node) uint32 {
		switch {
		case n == nil:
			return count
		case n.leaf:
			return count + 16 + uint32(n.data)
		default:
			return number[n]
		}
	}

	var out []byte
	for _, n := range inner {
		l, r := value(n.kids[0]), value(n.kids[1])
		switch opts.RecordSize {
		case 24:
			out = append(out, byte(l>>16), byte(l>>8), byte(l), byte(r>>16), byte(r>>8), byte(r))
		case 28:
			out = append(out, byte(l>>16), byte(l>>8), byte(l), byte(l>>24)<<4|byte(r>>24)&0x0f, byte(r>>16), byte(r>>8), byte(r))
		default:
			out = binary.BigEndian.AppendUint32(out, l)
			out = binary.BigEndian.AppendUint32(out, r)
		}
	}
	out = append(out, make([]byte, 16)...)
	out = append(out, data...)

	version := byte(6)
	if opts.IPv4Only {
		version = 4
	}
	out = append(out, "\xab\xcd\xefMaxMind.com"...)
	out = append(out, mapOf(6)...)
	out = append(out, str("binary_format_major_version")...)
	out = append(out, 0xa0|1, 2) // uint16
	out = append(out, str("build_epoch")...)
	out = append(out, 4, 2) // uint64 in four bytes, extended type
	out = binary.BigEndian.AppendUint32(out, 1790000000)
	out = append(out, str("database_type")...)
	out = append(out, str("Test-Country")...)
	out = append(out, str("ip_version")...)
	out = append(out, 0xa0|1, version)
	out = append(out, str("node_count")...)
	out = append(out, 0xc0|4)
	out = binary.BigEndian.AppendUint32(out, count)
	out = append(out, str("record_size")...)
	out = append(out, 0xa0|1, byte(opts.RecordSize))
	return out
}

func mapOf(pairs int) []byte { return []byte{0xe0 | byte(pairs)} }

func str(s string) []byte {
	if len(s) < 29 {
		return append([]byte{0x40 | byte(len(s))}, s...)
	}
	return append([]byte{0x40 | 29, byte(len(s) - 29)}, s...)
}

// pointer writes a pointer to an offset in the data section, in the smallest
// form that holds it.
func pointer(at int) []byte {
	switch {
	case at < 2048:
		return []byte{0x20 | byte(at>>8), byte(at)}
	case at < 2048+1<<19:
		at -= 2048
		return []byte{0x28 | byte(at>>16), byte(at >> 8), byte(at)}
	default:
		return []byte{0x38, byte(at >> 24), byte(at >> 16), byte(at >> 8), byte(at)}
	}
}
