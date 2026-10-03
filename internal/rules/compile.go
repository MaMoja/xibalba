package rules

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"net/textproto"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// headersSetByXibalba are rebuilt by Xibalba from the connection. Their
// incoming values are whatever the client chose to send, so a rule on them
// would be a rule on attacker-controlled text that looks like an address.
var headersSetByXibalba = map[string]bool{
	"X-Forwarded-For":   true,
	"X-Forwarded-Host":  true,
	"X-Forwarded-Proto": true,
	"X-Real-Ip":         true,
	"Forwarded":         true,
}

// Compile checks spec and turns it into an Engine. If it finds mistakes it
// returns all of them and no engine.
func Compile(spec Spec) (*Engine, []Problem) {
	c := &compiler{}
	e := &Engine{}

	if !decides(spec.DefaultAction) {
		c.global("default_action", fmt.Sprintf("%q is not an action a default can take", spec.DefaultAction),
			"use one of: allow, deny, challenge")
	}

	if len(spec.Rules) > MaxRules {
		c.global("", fmt.Sprintf("the rule set has %d rules; the limit is %d", len(spec.Rules), MaxRules),
			"remove rules, or combine similar ones with any")
		return nil, c.problems
	}

	seen := map[string]int{}
	for i, rs := range spec.Rules {
		c.rule = i
		rule := compiledRule{source: -1, action: rs.Action, weight: rs.Weight}

		switch {
		case rs.Name == "":
			c.add("name", "the rule has no name", `give it a short unique name such as "block-example-bot"`)
		case !nameRE.MatchString(rs.Name):
			c.add("name", fmt.Sprintf("%q is not a valid rule name", rs.Name),
				"use lower-case letters, digits, dot, underscore and hyphen, starting with a letter or digit, at most 64 characters")
		default:
			if first, dup := seen[rs.Name]; dup {
				c.add("name", fmt.Sprintf("the name %q is already used by rule number %d", rs.Name, first+1),
					"give every rule its own name")
			}
			seen[rs.Name] = i
		}

		switch {
		case rs.Action == Weigh:
			if rs.Weight == 0 {
				c.add("weight", "a weigh rule needs a weight", "set weight to a number other than 0, for example 5 or -5")
			} else if rs.Weight > MaxWeight || rs.Weight < -MaxWeight {
				c.add("weight", fmt.Sprintf("%d is out of range", rs.Weight),
					fmt.Sprintf("use a weight from %d to %d", -MaxWeight, MaxWeight))
			}
		case decides(rs.Action):
			if rs.Weight != 0 {
				c.add("weight", fmt.Sprintf("weight is only used with action weigh, but the action is %s", rs.Action),
					"remove weight, or change the action to weigh")
			}
		case rs.Action == "":
			c.add("action", "the rule has no action", "use one of: allow, deny, challenge, weigh")
		default:
			c.add("action", fmt.Sprintf("%q is not an action", rs.Action), "use one of: allow, deny, challenge, weigh")
		}

		rule.match = c.match(rs.Match, "match", 1, true)

		if decides(rs.Action) {
			rule.source = e.addSource("rule:"+rs.Name, rs.Action)
		}
		e.rules = append(e.rules, rule)
	}

	c.rule = -1
	weights := map[int]int{}
	for i, ts := range spec.Thresholds {
		field := fmt.Sprintf("thresholds[%d]", i)
		ok := true
		if ts.Weight < 1 || ts.Weight > MaxWeight {
			c.global(field+".weight", fmt.Sprintf("%d is out of range", ts.Weight),
				fmt.Sprintf("use a weight from 1 to %d", MaxWeight))
			ok = false
		} else if first, dup := weights[ts.Weight]; dup {
			c.global(field+".weight", fmt.Sprintf("weight %d is already used by threshold number %d", ts.Weight, first+1),
				"give every threshold its own weight")
			ok = false
		}
		weights[ts.Weight] = i
		if ts.Action != Challenge && ts.Action != Deny {
			c.global(field+".action", fmt.Sprintf("%q is not an action a threshold can take", ts.Action),
				"use challenge or deny")
			ok = false
		}
		if ok {
			e.thresholds = append(e.thresholds, threshold{
				weight: ts.Weight,
				source: e.addSource("threshold:"+strconv.Itoa(ts.Weight), ts.Action),
			})
		}
	}
	// Highest first, so the first one the score reaches is the strictest.
	sort.SliceStable(e.thresholds, func(a, b int) bool { return e.thresholds[a].weight > e.thresholds[b].weight })

	e.defaultSource = e.addSource("default", spec.DefaultAction)

	if len(c.problems) > 0 {
		return nil, c.problems
	}
	return e, nil
}

func decides(a Action) bool { return a == Allow || a == Deny || a == Challenge }

func (e *Engine) addSource(id string, action Action) int {
	sum := sha256.Sum256([]byte(id))
	e.sources = append(e.sources, Source{ID: id, Action: action, Reference: hex.EncodeToString(sum[:4])})
	return len(e.sources) - 1
}

type compiler struct {
	rule     int
	problems []Problem
}

func (c *compiler) add(field, message, hint string) {
	c.problems = append(c.problems, Problem{Rule: c.rule, Field: field, Message: message, Hint: hint})
}

func (c *compiler) global(field, message, hint string) {
	c.problems = append(c.problems, Problem{Rule: -1, Field: field, Message: message, Hint: hint})
}

// match compiles one group of conditions. Cheap conditions come first so an
// expensive one, such as a regular expression, only runs when the rest holds.
func (c *compiler) match(spec MatchSpec, field string, depth int, top bool) matcher {
	if depth > MaxDepth {
		c.add(field, fmt.Sprintf("conditions are nested more than %d levels deep", MaxDepth),
			"flatten the rule, or split it into several rules")
		return never{}
	}

	var parts allOf

	if spec.Method != nil {
		methods := make(methodIn, 0, len(spec.Method))
		if len(spec.Method) == 0 || len(spec.Method) > MaxConditions {
			c.add(field+".method", fmt.Sprintf("the list has %d entries; it needs 1 to %d", len(spec.Method), MaxConditions),
				`list methods such as ["GET", "HEAD"]`)
		}
		for i, m := range spec.Method {
			up := strings.ToUpper(strings.TrimSpace(m))
			if !isToken(up) {
				c.add(fmt.Sprintf("%s.method[%d]", field, i), fmt.Sprintf("%q is not an HTTP method", m),
					`use a method such as "GET" or "POST"`)
				continue
			}
			methods = append(methods, up)
		}
		parts = append(parts, methods)
	}

	if spec.Host != nil {
		if spec.Host.CaseSensitive {
			c.add(field+".host.case_sensitive", "host names are never case sensitive", "remove case_sensitive")
		}
		if sm, ok := c.text(*spec.Host, field+".host", false); ok {
			parts = append(parts, fieldMatch{field: fieldHost, text: sm})
		}
	}
	if spec.Path != nil {
		if sm, ok := c.text(*spec.Path, field+".path", false); ok {
			parts = append(parts, fieldMatch{field: fieldPath, text: sm})
		}
	}
	if spec.UserAgent != nil {
		if sm, ok := c.text(*spec.UserAgent, field+".user_agent", false); ok {
			parts = append(parts, fieldMatch{field: fieldUserAgent, text: sm})
		}
	}

	if spec.Header != nil {
		if len(spec.Header) == 0 || len(spec.Header) > MaxConditions {
			c.add(field+".header", fmt.Sprintf("the list has %d entries; it needs 1 to %d", len(spec.Header), MaxConditions),
				"name at least one header, or remove header")
		}
		names := make([]string, 0, len(spec.Header))
		for name := range spec.Header {
			names = append(names, name)
		}
		sort.Strings(names) // map order is random; evaluation order must not be
		for _, name := range names {
			hfield := field + ".header." + name
			canonical := textproto.CanonicalMIMEHeaderKey(name)
			switch {
			case !isToken(name):
				c.add(hfield, fmt.Sprintf("%q is not a header name", name), `use a name such as "Accept-Language"`)
				continue
			case headersSetByXibalba[canonical]:
				c.add(hfield, fmt.Sprintf("%s is written by the client and cannot be trusted as an address", canonical),
					"use the ip condition, which tests the address Xibalba established itself")
				continue
			case canonical == "User-Agent":
				c.add(hfield, "the user agent has its own condition", "use user_agent instead of header")
				continue
			case canonical == "Host":
				c.add(hfield, "the host has its own condition", "use host instead of header")
				continue
			}
			hs := spec.Header[name]
			if hs == nil {
				c.add(hfield, "the header has no test", `add one, for example {present: true} or {contains: "text"}`)
				continue
			}
			if sm, ok := c.text(*hs, hfield, true); ok {
				parts = append(parts, headerMatch{name: canonical, text: sm})
			}
		}
	}

	if spec.IP != nil {
		if len(spec.IP) == 0 || len(spec.IP) > MaxConditions {
			c.add(field+".ip", fmt.Sprintf("the list has %d entries; it needs 1 to %d", len(spec.IP), MaxConditions),
				`list addresses or networks such as ["192.0.2.7", "2001:db8::/32"]`)
		}
		prefixes := make(ipIn, 0, len(spec.IP))
		for i, entry := range spec.IP {
			prefix, err := parsePrefix(entry)
			if err != nil {
				c.add(fmt.Sprintf("%s.ip[%d]", field, i), fmt.Sprintf("%q is not an IP address or network", entry),
					`use an address such as "192.0.2.7" or a network such as "10.0.0.0/8"`)
				continue
			}
			prefixes = append(prefixes, prefix)
		}
		parts = append(parts, prefixes)
	}

	if spec.All != nil {
		parts = append(parts, c.group(spec.All, field+".all", depth, func(ms []matcher) matcher { return allOf(ms) }))
	}
	if spec.Any != nil {
		parts = append(parts, c.group(spec.Any, field+".any", depth, func(ms []matcher) matcher { return anyOf(ms) }))
	}
	if spec.Not != nil {
		parts = append(parts, notOf{c.match(*spec.Not, field+".not", depth+1, false)})
	}

	// A condition that was written but is invalid has already been reported.
	// Saying "no conditions" on top of that would send the reader looking for
	// a second mistake that does not exist.
	declared := spec.Method != nil || spec.Host != nil || spec.Path != nil || spec.UserAgent != nil ||
		spec.Header != nil || spec.IP != nil || spec.All != nil || spec.Any != nil || spec.Not != nil

	switch len(parts) {
	case 0:
		if declared {
			return never{}
		}
		if top {
			c.add(field, "the rule has no conditions, so it would match every request",
				"add a condition, or set default_action if every remaining request should get this action")
		} else {
			c.add(field, "this group has no conditions", "add a condition or remove the group")
		}
		return never{}
	case 1:
		return parts[0]
	default:
		return parts
	}
}

func (c *compiler) group(specs []MatchSpec, field string, depth int, build func([]matcher) matcher) matcher {
	if len(specs) == 0 || len(specs) > MaxConditions {
		c.add(field, fmt.Sprintf("the list has %d entries; it needs 1 to %d", len(specs), MaxConditions),
			"add at least one group of conditions, or remove the list")
		return never{}
	}
	ms := make([]matcher, 0, len(specs))
	for i, s := range specs {
		ms = append(ms, c.match(s, fmt.Sprintf("%s[%d]", field, i), depth+1, false))
	}
	return build(ms)
}

// text compiles a StringSpec. header says whether present is allowed.
func (c *compiler) text(spec StringSpec, field string, header bool) (textMatcher, bool) {
	type candidate struct {
		name  string
		value string
		op    op
	}
	var set []candidate
	for _, cand := range []candidate{
		{"equals", spec.Equals, opEquals},
		{"contains", spec.Contains, opContains},
		{"prefix", spec.Prefix, opPrefix},
		{"suffix", spec.Suffix, opSuffix},
		{"regex", spec.Regex, opRegex},
	} {
		if cand.value != "" {
			set = append(set, cand)
		}
	}

	const tests = "equals, contains, prefix, suffix, regex"
	if spec.Present != nil {
		switch {
		case !header:
			c.add(field+".present", "present can only be used with a header", "use one of: "+tests)
			return textMatcher{}, false
		case len(set) > 0:
			c.add(field, "present cannot be combined with "+set[0].name, "use one test per header")
			return textMatcher{}, false
		case spec.CaseSensitive:
			c.add(field+".case_sensitive", "case_sensitive has no meaning with present", "remove case_sensitive")
			return textMatcher{}, false
		}
		if *spec.Present {
			return textMatcher{op: opPresent}, true
		}
		return textMatcher{op: opAbsent}, true
	}

	switch len(set) {
	case 0:
		hint := "use one of: " + tests
		if header {
			hint += ", present"
		}
		c.add(field, "no test is given", hint)
		return textMatcher{}, false
	case 1:
	default:
		names := make([]string, len(set))
		for i, s := range set {
			names[i] = s.name
		}
		c.add(field, "several tests are given: "+strings.Join(names, ", "),
			"keep one; to combine tests use all or any")
		return textMatcher{}, false
	}

	chosen := set[0]
	sub := field + "." + chosen.name
	if len(chosen.value) > MaxPatternLength {
		c.add(sub, fmt.Sprintf("the text is %d characters long; the limit is %d", len(chosen.value), MaxPatternLength),
			"shorten it")
		return textMatcher{}, false
	}

	fold := !spec.CaseSensitive
	tm := textMatcher{op: chosen.op, fold: fold, needle: chosen.value}
	if chosen.op == opRegex {
		// Check the expression as written, so an error quotes what the
		// author typed and not the form used internally.
		_, err := regexp.Compile(chosen.value)
		pattern := chosen.value
		if fold {
			pattern = "(?i)" + pattern
		}
		var re *regexp.Regexp
		if err == nil {
			re, err = regexp.Compile(pattern)
		}
		if err != nil {
			c.add(sub, "the regular expression is not valid: "+strings.TrimPrefix(err.Error(), "error parsing regexp: "),
				"Xibalba uses RE2 syntax, which has no lookahead or backreferences; for plain text use contains")
			return textMatcher{}, false
		}
		tm.re = re
		return tm, true
	}
	if fold {
		tm.needle = asciiLower(chosen.value)
	}
	return tm, true
}

// parsePrefix reads an address or a network in CIDR notation. A single
// address becomes a network containing only it. IPv4-mapped IPv6 addresses
// are treated as IPv4, matching how client addresses are normalised.
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

// isToken reports whether s is an HTTP token, the syntax of method and header names.
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", ch) >= 0:
		default:
			return false
		}
	}
	return true
}
