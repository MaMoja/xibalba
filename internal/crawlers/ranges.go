package crawlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// Limits on a downloaded address list.
const (
	maxListBytes    = 2 << 20
	maxListPrefixes = 100000
)

// ParseRanges reads an address list as operators publish them. Two forms are
// understood:
//
//   - JSON of any shape: every text value that is an IP address or a network
//     in CIDR notation is taken. This covers the layout most operators use
//     ({"prefixes": [{"ipv4Prefix": "…"}, {"ipv6Prefix": "…"}]}) as well as
//     plain lists, without depending on any operator's key names.
//   - Plain text: one address or network per line; blank lines and lines
//     starting with "#" are ignored.
//
// The list is refused as a whole if it is empty, too long, or contains a
// network that cannot belong to one crawler (see plausible) or is not public. A partly usable
// list is not used: better to keep yesterday's good list than half of today's.
func ParseRanges(data []byte) ([]netip.Prefix, error) {
	if len(data) > maxListBytes {
		return nil, fmt.Errorf("the list is larger than %d MiB", maxListBytes>>20)
	}
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")))
	if len(trimmed) == 0 {
		return nil, errors.New("the list is empty")
	}

	var texts []string
	if trimmed[0] == '{' || trimmed[0] == '[' {
		var doc any
		if err := json.Unmarshal(trimmed, &doc); err != nil {
			return nil, errors.New("the list is not valid JSON")
		}
		collectStrings(doc, &texts, 0)
	} else {
		for _, line := range strings.Split(string(trimmed), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if _, err := parsePrefix(line); err != nil {
				// The content is not repeated: it comes from another server
				// and ends up in the log and the health report.
				return nil, errors.New("the list has a line that is not an address or network")
			}
			texts = append(texts, line)
		}
	}

	seen := map[netip.Prefix]bool{}
	var prefixes []netip.Prefix
	for _, text := range texts {
		prefix, err := parsePrefix(text)
		if err != nil {
			continue // some other text in the JSON, such as a date
		}
		if err := plausible(prefix); err != nil {
			return nil, fmt.Errorf("the list contains %s: %v", prefix, err)
		}
		// A crawler on the internet does not come from a private network. A
		// published list that says so would make local clients "genuine".
		if a := prefix.Addr(); a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsMulticast() {
			return nil, fmt.Errorf("the list contains %s, which is not a public address", prefix)
		}
		if !seen[prefix] {
			seen[prefix] = true
			prefixes = append(prefixes, prefix)
		}
		if len(prefixes) > maxListPrefixes {
			return nil, fmt.Errorf("the list has more than %d entries", maxListPrefixes)
		}
	}
	if len(prefixes) == 0 {
		return nil, errors.New("the list contains no addresses")
	}
	sort.Slice(prefixes, func(a, b int) bool {
		if c := prefixes[a].Addr().Compare(prefixes[b].Addr()); c != 0 {
			return c < 0
		}
		return prefixes[a].Bits() < prefixes[b].Bits()
	})
	return prefixes, nil
}

// collectStrings gathers every text value in a decoded JSON document.
func collectStrings(v any, out *[]string, depth int) {
	if depth > 32 {
		return
	}
	switch value := v.(type) {
	case string:
		// Only texts that can be an address are worth keeping.
		if len(value) <= 64 && (strings.ContainsAny(value, ".:")) {
			*out = append(*out, value)
		}
	case []any:
		for _, item := range value {
			collectStrings(item, out, depth+1)
		}
	case map[string]any:
		for _, item := range value {
			collectStrings(item, out, depth+1)
		}
	}
}

// contains reports whether addr is in one of the prefixes.
func contains(prefixes []netip.Prefix, addr netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
