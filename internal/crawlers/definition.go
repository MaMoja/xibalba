// Package crawlers knows the crawlers of the web: who runs them, what they
// are for, and how to tell a genuine one from an impostor.
//
// A crawler announces itself in its user agent, and anyone can send any user
// agent. The package therefore answers two separate questions about a
// request: which crawler does it claim to be, and does it really come from
// that crawler's operator? The second is decided by the operator's own
// published addresses or by reverse DNS, never by the name.
//
// The definitions are data files (data/crawlers), each naming the operator's
// page it was taken from. Address lists are downloaded in the background and
// DNS lookups run in the background; identifying a request never waits for
// the network.
package crawlers

import (
	"fmt"
	"io/fs"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

// Class says what a crawler is for.
type Class string

const (
	// Training crawlers collect pages for training AI models.
	Training Class = "training"
	// AISearch crawlers build an index for AI answers that link to their sources.
	AISearch Class = "ai-search"
	// UserFetch fetchers load a page because a person just asked for it.
	UserFetch Class = "user-fetch"
	// SearchEngine crawlers build the index of a classic search engine.
	SearchEngine Class = "search-engine"
	// Archive crawlers build a public copy of the web for anyone to use.
	Archive Class = "archive"
	// Other is everything else: previews, advertising checks, site tools.
	Other Class = "other"
)

// Classes lists every class, in the order documentation shows them.
var Classes = []Class{Training, AISearch, UserFetch, SearchEngine, Archive, Other}

// Definition describes one crawler.
type Definition struct {
	// Name identifies the crawler in rules and statistics.
	Name string `yaml:"name"`
	// Class says what the crawler is for.
	Class Class `yaml:"class"`
	// UserAgent is the text that appears in the crawler's user agent. A
	// request claims to be this crawler if its user agent contains it,
	// ignoring case.
	UserAgent string `yaml:"user_agent"`
	// Purpose says, in one sentence, what the operator uses the crawler for.
	Purpose string `yaml:"purpose"`
	// Note holds anything a site owner should know beyond the purpose.
	Note string `yaml:"note"`
	// Verify says how a genuine request is recognised.
	Verify Verify `yaml:"verify"`

	// Operator, Source and Checked come from the file the definition is in.
	Operator string `yaml:"-"`
	Source   string `yaml:"-"`
	Checked  string `yaml:"-"`
}

// Verify says how a genuine request from a crawler is recognised. Several
// ways may be given; passing any one is enough. With none, the crawler cannot
// be verified and is never treated as genuine.
type Verify struct {
	// RangesURL is where the operator publishes the crawler's addresses.
	RangesURL string `yaml:"ranges_url"`
	// Ranges lists the crawler's addresses directly.
	Ranges []string `yaml:"ranges"`
	// ReverseDNS lists the domain endings a genuine address resolves to,
	// each starting with a dot, such as ".googlebot.com".
	ReverseDNS []string `yaml:"reverse_dns"`
	// Source is the operator's page that describes the verification, if it
	// is a different page from the one that names the crawler.
	Source string `yaml:"source"`

	prefixes []netip.Prefix // Ranges, parsed
}

// Method names how the crawler is verified, for display.
func (v Verify) Method() string {
	var parts []string
	if v.RangesURL != "" || len(v.Ranges) > 0 {
		parts = append(parts, "addresses")
	}
	if len(v.ReverseDNS) > 0 {
		parts = append(parts, "reverse DNS")
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// Verifiable reports whether there is any way to verify the crawler.
func (v Verify) Verifiable() bool {
	return v.RangesURL != "" || len(v.Ranges) > 0 || len(v.ReverseDNS) > 0
}

// file is the layout of a definition file: one operator and its crawlers.
type file struct {
	Operator string       `yaml:"operator"`
	Source   string       `yaml:"source"`
	Checked  string       `yaml:"checked"`
	Crawlers []Definition `yaml:"crawlers"`
}

// Problem is one mistake in a definition file.
type Problem struct {
	// File is the file the problem is in.
	File string
	// Field is the path of the offending setting, such as "crawlers[1].verify.ranges_url".
	Field string
	// Message says what is wrong.
	Message string
	// Hint says how to fix it.
	Hint string
}

var (
	nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	// A domain ending: a dot, then at least two labels.
	suffixRE = regexp.MustCompile(`^(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?){2,}$`)
)

// Limits on a definition file.
const (
	maxFileSize        = 1 << 20
	maxCrawlersPerFile = 500
	maxStaticRanges    = 10000
)

// ParseFile reads one definition file and checks it. name is used in problems.
func ParseFile(name string, data []byte) ([]Definition, []Problem) {
	if len(data) > maxFileSize {
		return nil, []Problem{{File: name, Message: fmt.Sprintf("the file is larger than %d KiB", maxFileSize>>10), Hint: "split it into several files"}}
	}
	var f file
	if err := yaml.UnmarshalWithOptions(data, &f, yaml.Strict()); err != nil {
		return nil, []Problem{{
			File:    name,
			Message: "the file is not valid: " + strings.TrimSpace(yaml.FormatError(err, false, true)),
			Hint:    "a crawler file has the keys operator, source, checked and crawlers; see docs/CRAWLERS.md",
		}}
	}

	var problems []Problem
	add := func(field, message, hint string) {
		problems = append(problems, Problem{File: name, Field: field, Message: message, Hint: hint})
	}

	if strings.TrimSpace(f.Operator) == "" {
		add("operator", "the operator is missing", "name who runs these crawlers")
	}
	if !isHTTPURL(f.Source) {
		add("source", fmt.Sprintf("%q is not a web address", f.Source),
			"give the page of the operator's own documentation this file was taken from")
	}
	if _, err := time.Parse("2006-01-02", f.Checked); err != nil {
		add("checked", fmt.Sprintf("%q is not a date", f.Checked), "write the day the source was last checked as YYYY-MM-DD")
	}
	if len(f.Crawlers) == 0 {
		add("crawlers", "the file defines no crawler", "add at least one entry under crawlers")
	}
	if len(f.Crawlers) > maxCrawlersPerFile {
		add("crawlers", fmt.Sprintf("the file defines %d crawlers; the limit is %d", len(f.Crawlers), maxCrawlersPerFile), "split it into several files")
		return nil, problems
	}

	for i := range f.Crawlers {
		d := &f.Crawlers[i]
		field := fmt.Sprintf("crawlers[%d]", i)
		d.Operator, d.Source, d.Checked = f.Operator, f.Source, f.Checked

		if !nameRE.MatchString(d.Name) {
			add(field+".name", fmt.Sprintf("%q is not a valid crawler name", d.Name),
				"use letters, digits, dot, underscore and hyphen, at most 64 characters")
		}
		if !knownClass(d.Class) {
			add(field+".class", fmt.Sprintf("%q is not a class", d.Class), "use one of: "+classList())
		}
		if len(strings.TrimSpace(d.UserAgent)) < 3 || len(d.UserAgent) > 100 {
			add(field+".user_agent", fmt.Sprintf("%q cannot identify a crawler", d.UserAgent),
				"give the text that appears in the crawler's user agent, 3 to 100 characters")
		}
		if strings.TrimSpace(d.Purpose) == "" {
			add(field+".purpose", "the purpose is missing", "say in one sentence what the operator uses the crawler for")
		}

		v := &d.Verify
		if v.RangesURL != "" {
			if err := checkRangesURL(v.RangesURL); err != nil {
				add(field+".verify.ranges_url", fmt.Sprintf("%q cannot be used: %v", v.RangesURL, err),
					"give the https address where the operator publishes the crawler's addresses")
			}
		}
		if len(v.Ranges) > maxStaticRanges {
			add(field+".verify.ranges", fmt.Sprintf("the list has %d entries; the limit is %d", len(v.Ranges), maxStaticRanges), "shorten the list")
		} else {
			for j, entry := range v.Ranges {
				prefix, err := parsePrefix(entry)
				if err != nil {
					add(fmt.Sprintf("%s.verify.ranges[%d]", field, j), fmt.Sprintf("%q is not an IP address or network", entry),
						`use an address such as "192.0.2.7" or a network such as "192.0.2.0/24"`)
					continue
				}
				if err := plausible(prefix); err != nil {
					add(fmt.Sprintf("%s.verify.ranges[%d]", field, j), fmt.Sprintf("%q is not accepted: %v", entry, err),
						"list the crawler's own networks only")
					continue
				}
				v.prefixes = append(v.prefixes, prefix)
			}
		}
		for j, suffix := range v.ReverseDNS {
			if !suffixRE.MatchString(suffix) {
				add(fmt.Sprintf("%s.verify.reverse_dns[%d]", field, j), fmt.Sprintf("%q is not a domain ending", suffix),
					`write it in lower case with a leading dot and at least two parts, such as ".googlebot.com"`)
			}
		}
		if v.Source != "" && !isHTTPURL(v.Source) {
			add(field+".verify.source", fmt.Sprintf("%q is not a web address", v.Source), "give the operator's page that describes the verification")
		}
	}
	if len(problems) > 0 {
		return nil, problems
	}
	return f.Crawlers, nil
}

// LoadFS reads every *.yaml file in dir of fsys, in name order.
func LoadFS(fsys fs.FS, dir string) ([]Definition, []Problem) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, []Problem{{File: dir, Message: "the crawler definitions cannot be read: " + err.Error()}}
	}
	var defs []Definition
	var problems []Problem
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		path := dir + "/" + entry.Name()
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			problems = append(problems, Problem{File: path, Message: "the file cannot be read: " + err.Error()})
			continue
		}
		found, p := ParseFile(path, data)
		defs = append(defs, found...)
		problems = append(problems, p...)
	}
	return defs, problems
}

// CheckUnique reports crawlers that share a name or a user agent text.
// Names are compared without regard to case. files maps each definition to
// the file it came from, for the message.
func CheckUnique(defs []Definition, origin func(i int) (file, field string)) []Problem {
	var problems []Problem
	names, agents := map[string]int{}, map[string]int{}
	for i, d := range defs {
		file, field := origin(i)
		if first, dup := names[strings.ToLower(d.Name)]; dup {
			problems = append(problems, Problem{File: file, Field: field + ".name",
				Message: fmt.Sprintf("the crawler name %q is already defined by %s", d.Name, defs[first].Operator),
				Hint:    "give every crawler its own name"})
		} else {
			names[strings.ToLower(d.Name)] = i
		}
		if first, dup := agents[strings.ToLower(d.UserAgent)]; dup {
			problems = append(problems, Problem{File: file, Field: field + ".user_agent",
				Message: fmt.Sprintf("the user agent text %q is already used by the crawler %s", d.UserAgent, defs[first].Name),
				Hint:    "two crawlers cannot be told apart by the same text"})
		} else {
			agents[strings.ToLower(d.UserAgent)] = i
		}
	}
	return problems
}

// Names returns the crawler names in defs, sorted.
func Names(defs []Definition) []string {
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.Name
	}
	sort.Strings(names)
	return names
}

func knownClass(c Class) bool {
	for _, known := range Classes {
		if c == known {
			return true
		}
	}
	return false
}

func classList() string {
	names := make([]string, len(Classes))
	for i, c := range Classes {
		names[i] = string(c)
	}
	return strings.Join(names, ", ")
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// checkRangesURL accepts https addresses, and plain http only for this
// machine itself. An address list fetched over an unprotected connection
// could be replaced on the way, and whoever replaces it decides which
// addresses count as a genuine crawler.
func checkRangesURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("it is not a web address")
	}
	if u.User != nil {
		return fmt.Errorf("it must not contain a user name or password")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "localhost" {
			return nil
		}
		if addr, err := netip.ParseAddr(host); err == nil && addr.IsLoopback() {
			return nil
		}
		return fmt.Errorf("it must use https, so the list cannot be replaced on the way")
	default:
		return fmt.Errorf("it must start with https://")
	}
}

// parsePrefix reads an address or a network. A single address becomes a
// network containing only it; IPv4-mapped IPv6 is treated as IPv4.
func parsePrefix(entry string) (netip.Prefix, error) {
	entry = strings.TrimSpace(entry)
	if prefix, err := netip.ParsePrefix(entry); err == nil {
		if prefix.Addr().Is4In6() {
			bits := prefix.Bits() - 96
			if bits < 0 {
				return netip.Prefix{}, fmt.Errorf("an IPv4-mapped network must be /96 or longer")
			}
			return netip.PrefixFrom(prefix.Addr().Unmap(), bits).Masked(), nil
		}
		return prefix.Masked(), nil
	}
	addr, err := netip.ParseAddr(entry)
	if err != nil {
		return netip.Prefix{}, err
	}
	addr = addr.Unmap().WithZone("")
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

// plausible rejects networks that cannot belong to one crawler. A list that
// contained, say, 0.0.0.0/0 would make every client on the internet a
// "verified" crawler, so such an entry invalidates the list it is in.
func plausible(p netip.Prefix) error {
	addr := p.Addr()
	switch {
	case addr.Is4() && p.Bits() < 8:
		return fmt.Errorf("an IPv4 network larger than /8 covers too much of the internet")
	case addr.Is6() && p.Bits() < 24:
		return fmt.Errorf("an IPv6 network larger than /24 covers too much of the internet")
	case addr.IsUnspecified():
		return fmt.Errorf("it is the unspecified address")
	}
	return nil
}
