package rules

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"net/textproto"
	"regexp"
	"regexp/syntax"
	"sort"
	"strconv"
	"strings"
	"time"
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
	c := &compiler{catalog: spec.Crawlers, trap: spec.Trap, countries: spec.Countries}
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

		if rs.Challenge != nil {
			if rs.Action != Challenge {
				c.add("challenge", fmt.Sprintf("a security check is described, but the action is %s", rs.Action),
					"remove challenge, or change the action to challenge")
			} else {
				c.challenge(rs.Challenge, func(field, message, hint string) { c.add("challenge."+field, message, hint) })
			}
		}

		// A rule that lets requests through, or makes them look better.
		c.favours = rs.Action == Allow || (rs.Action == Weigh && rs.Weight < 0)
		c.negated = false
		before := c.countryConditions
		c.anchored = false
		rule.match = c.match(rs.Match, "match", 1, true)
		rule.needsCountry = c.countryConditions > before

		if rs.ExemptFromLimits && rs.Action == Allow && !c.anchored {
			c.add("exempt_from_limits", "the rule exempts from the limits whoever matches it, and anyone can choose to match it",
				"exempt only by something the client cannot choose: add an ip condition or a crawler condition with verified: true")
		}
		if rs.ExemptFromLimits && rs.Action != Allow {
			c.add("exempt_from_limits", fmt.Sprintf("only an allow rule can exempt from the limits, but the action is %s", rs.Action),
				"remove exempt_from_limits, or change the action to allow")
		}

		if decides(rs.Action) {
			rule.source = e.addSource("rule:"+rs.Name, rs.Action)
			e.sources[rule.source].Challenge = rs.Challenge
			e.sources[rule.source].ExemptFromLimits = rs.ExemptFromLimits && rs.Action == Allow
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
		if ts.Challenge != nil {
			report := func(sub, message, hint string) { c.global(field+".challenge."+sub, message, hint) }
			if ts.Action != Challenge {
				c.global(field+".challenge", fmt.Sprintf("a security check is described, but the action is %s", ts.Action),
					"remove challenge, or change the action to challenge")
			} else {
				c.challenge(ts.Challenge, report)
			}
		}
		if ts.Action != Challenge && ts.Action != Deny {
			c.global(field+".action", fmt.Sprintf("%q is not an action a threshold can take", ts.Action),
				"use challenge or deny")
			ok = false
		}
		if ok {
			e.thresholds = append(e.thresholds, threshold{
				weight: ts.Weight,
				source: e.addThreshold(ts),
			})
		}
	}
	// Highest first, so the first one the score reaches is the strictest.
	sort.SliceStable(e.thresholds, func(a, b int) bool { return e.thresholds[a].weight > e.thresholds[b].weight })

	e.defaultSource = e.addSource("default", spec.DefaultAction)

	if len(c.problems) > 0 {
		return nil, c.problems
	}
	e.usesCrawlers = c.usesCrawlers
	e.usesTrap = c.usesTrap
	e.usesCountry = c.usesCountry
	return e, nil
}

func decides(a Action) bool { return a == Allow || a == Deny || a == Challenge }

func (e *Engine) addThreshold(ts ThresholdSpec) int {
	source := e.addSource("threshold:"+strconv.Itoa(ts.Weight), ts.Action)
	if ts.Action == Challenge {
		e.sources[source].Challenge = ts.Challenge
	}
	return source
}

// challenge checks the description of a security check. What is left out
// is filled in from the default later, so only what is written is checked.
func (c *compiler) challenge(spec *ChallengeSpec, add func(field, message, hint string)) {
	known := func(list []string, v string) bool {
		for _, item := range list {
			if item == v {
				return true
			}
		}
		return false
	}
	if spec.Method != "" && !known(ChallengeMethods, spec.Method) {
		add("method", fmt.Sprintf("%q is not a kind of security check", spec.Method), "use one of: "+strings.Join(ChallengeMethods, ", "))
	}
	if spec.Difficulty != 0 && (spec.Difficulty < MinChallengeDifficulty || spec.Difficulty > MaxChallengeDifficulty) {
		add("difficulty", fmt.Sprintf("%d is out of range", spec.Difficulty),
			fmt.Sprintf("use a value from %d to %d; each step doubles the work", MinChallengeDifficulty, MaxChallengeDifficulty))
	}
	if spec.Difficulty != 0 && spec.Method != "" && spec.Method != "pow" {
		add("difficulty", fmt.Sprintf("difficulty belongs to the method pow, but the method is %s", spec.Method), "remove difficulty, or use method: pow")
	}
	if spec.Wait != 0 && (spec.Wait < time.Second || spec.Wait > MaxChallengeWait) {
		add("wait", fmt.Sprintf("%s is out of range", spec.Wait), `use a duration from "1s" to "1m"`)
	}
	if len(spec.Checks) > len(ChallengeChecks) {
		add("checks", "the list names a check more than once", "name each check once")
	}
	for i, check := range spec.Checks {
		if !known(ChallengeChecks, check) {
			add(fmt.Sprintf("checks[%d]", i), fmt.Sprintf("%q is not an extra check", check), "use: "+strings.Join(ChallengeChecks, ", "))
		}
	}
	if len(spec.Checks) > 0 && (spec.Method == "wait" || spec.Method == "refresh") {
		add("checks", fmt.Sprintf("extra checks run in JavaScript, and the method %s does without it", spec.Method),
			"use method pow or script, or remove checks")
	}
	if spec.NoJavaScript != "" && spec.NoJavaScript != "button" && spec.NoJavaScript != "deny" {
		add("no_javascript", fmt.Sprintf("%q is not a mode", spec.NoJavaScript), "use button or deny")
	}
}

func (e *Engine) addSource(id string, action Action) int {
	sum := sha256.Sum256([]byte(id))
	e.sources = append(e.sources, Source{ID: id, Action: action, Reference: hex.EncodeToString(sum[:4])})
	return len(e.sources) - 1
}

type compiler struct {
	rule     int
	problems []Problem
	// groups counts the condition groups compiled, regexSize the steps of
	// the regular expressions; both are bounded for the whole rule set.
	groups    int
	regexSize int

	catalog     *Catalog
	trap        bool // the trap is switched on
	usesTrap    bool
	countries   bool // a country database is configured
	usesCountry bool
	// countryConditions counts the country conditions compiled so far.
	countryConditions int
	// anchored: the rule being compiled has a condition the client cannot
	// choose to meet: its address, or being a verified crawler.
	anchored     bool
	favours      bool // the rule being compiled allows, or lowers the score
	negated      bool // the conditions being compiled are inside an odd number of "not"
	usesCrawlers bool
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
	if c.groups++; c.groups > MaxGroups {
		if c.groups == MaxGroups+1 {
			c.global("rules", fmt.Sprintf("the rule set has more than %d groups of conditions", MaxGroups),
				"simplify the rules: nested all and any multiply")
		}
		return never{}
	}
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
			// Matching this condition favours the request if the rule
			// favours and the condition is not negated, or the rule
			// restricts and the condition is negated ("everyone except
			// this path"). Then only a plainly written address may match.
			parts = append(parts, fieldMatch{field: fieldPath, text: sm, strict: c.favours != c.negated})
		}
	}
	if spec.Query != nil {
		if sm, ok := c.text(*spec.Query, field+".query", true); ok {
			parts = append(parts, queryMatch{text: sm})
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
		if !c.negated {
			c.anchored = true
		}
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

	if spec.Crawler != nil {
		if m, ok := c.crawler(*spec.Crawler, field+".crawler"); ok {
			parts = append(parts, m)
		}
	}

	if spec.Country != nil {
		if m, ok := c.country(spec.Country, field+".country"); ok {
			parts = append(parts, m)
		}
	}

	if spec.Trapped != nil {
		if c.trap {
			parts = append(parts, trappedIs(*spec.Trapped))
			c.usesTrap = true
		} else {
			c.add(field+".trapped", "the trap is switched off, so this condition could never hold",
				"set trap.enabled to true, or remove the condition")
		}
	}

	if spec.All != nil {
		parts = append(parts, c.group(spec.All, field+".all", depth, func(ms []matcher) matcher { return allOf(ms) }))
	}
	if spec.Any != nil {
		parts = append(parts, c.group(spec.Any, field+".any", depth, func(ms []matcher) matcher { return anyOf(ms) }))
	}
	if spec.Not != nil {
		c.negated = !c.negated
		parts = append(parts, notOf{c.match(*spec.Not, field+".not", depth+1, false)})
		c.negated = !c.negated
	}

	// A condition that was written but is invalid has already been reported.
	// Saying "no conditions" on top of that would send the reader looking for
	// a second mistake that does not exist.
	declared := spec.Method != nil || spec.Host != nil || spec.Path != nil || spec.Query != nil || spec.UserAgent != nil ||
		spec.Header != nil || spec.IP != nil || spec.Crawler != nil || spec.Trapped != nil || spec.Country != nil || spec.All != nil || spec.Any != nil || spec.Not != nil

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

// country compiles a country condition.
func (c *compiler) country(list []string, field string) (matcher, bool) {
	if !c.countries {
		c.add(field, "no country database is configured, so this condition could never hold",
			"set countries.database to a database file; see docs/COUNTRIES.md")
		return nil, false
	}
	if len(list) == 0 || len(list) > MaxConditions {
		c.add(field, fmt.Sprintf("the list has %d entries; it needs 1 to %d", len(list), MaxConditions),
			`list country codes such as ["DE", "AT", "CH"]`)
		return nil, false
	}
	ok := true
	codes := make(countryIn, 0, len(list))
	for i, entry := range list {
		up := strings.ToUpper(strings.TrimSpace(entry))
		at := fmt.Sprintf("%s[%d]", field, i)
		switch {
		case len(up) != 2 || up[0] < 'A' || up[0] > 'Z' || up[1] < 'A' || up[1] > 'Z':
			c.add(at, fmt.Sprintf("%q is not a country code", entry), `use the two-letter code of ISO 3166-1, such as "DE" for Germany`)
			ok = false
		case up == "UK":
			c.add(at, `"UK" is not the code of the United Kingdom`, `use "GB"`)
			ok = false
		case up == "EU":
			c.add(at, `"EU" is not a country`, "list the countries you mean")
			ok = false
		default:
			codes = append(codes, [2]byte{up[0], up[1]})
		}
	}
	if !ok {
		return nil, false
	}
	c.usesCountry = true
	c.countryConditions++
	return codes, true
}

// crawler compiles a crawler condition.
func (c *compiler) crawler(spec CrawlerSpec, field string) (matcher, bool) {
	if c.catalog == nil {
		c.add(field, "crawler conditions cannot be used here, because no crawler definitions are loaded",
			"use user_agent and ip conditions instead")
		return nil, false
	}
	ok := true
	if spec.Class == nil && spec.Name == nil && spec.Verified == nil {
		c.add(field, "the crawler condition is empty", `say which crawlers are meant, for example {class: [training]} or {name: [GPTBot]}`)
		ok = false
	}
	m := crawlerMatch{any: spec.Verified == nil}
	if spec.Verified != nil {
		m.status = CrawlerImpostor
		if *spec.Verified {
			m.status = CrawlerVerified
		}
	}
	pick := func(list []string, known []string, sub, what string) []string {
		if list == nil {
			return nil
		}
		if len(list) == 0 || len(list) > MaxConditions {
			c.add(field+"."+sub, fmt.Sprintf("the list has %d entries; it needs 1 to %d", len(list), MaxConditions),
				"name at least one "+what+", or remove "+sub)
			ok = false
		}
		out := make([]string, 0, len(list))
		for i, entry := range list {
			found := ""
			for _, k := range known {
				if strings.EqualFold(k, strings.TrimSpace(entry)) {
					found = k
					break
				}
			}
			if found == "" {
				hint := "use one of: " + strings.Join(known, ", ")
				if sub == "name" {
					hint = "the known crawlers are listed in docs/CRAWLERS.md and at /crawlers on the operations listener; " +
						"for any other program use a user_agent condition"
				}
				c.add(fmt.Sprintf("%s.%s[%d]", field, sub, i), fmt.Sprintf("%q is not a known crawler %s", entry, what), hint)
				ok = false
				continue
			}
			out = append(out, found)
		}
		return out
	}
	m.classes = pick(spec.Class, c.catalog.Classes, "class", "class")
	m.names = pick(spec.Name, c.catalog.Names, "name", "name")

	// The name in a user agent proves nothing: anyone can send it. No rule
	// may treat a request better because of the name alone. That happens in
	// two ways: a rule that lets through (or lowers the score of) whoever
	// carries the name, and a rule that restricts everyone except whoever
	// carries the name. Either way the condition must insist on a genuine
	// crawler.
	if c.favours != c.negated && (spec.Verified == nil || !*spec.Verified) {
		message := "a rule that lets a crawler through must make sure it is genuine, because anyone can send a crawler's name"
		if c.negated {
			message = "a rule that spares a crawler must make sure it is genuine, because anyone can send a crawler's name"
		}
		c.add(field+".verified", message, "add verified: true to the crawler condition")
		ok = false
	}
	if !ok {
		return nil, false
	}
	if !c.negated && spec.Verified != nil && *spec.Verified {
		c.anchored = true
	}
	c.usesCrawlers = true
	return m, true
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
			c.add(field+".present", "present can only be used with a header or the query", "use one of: "+tests)
			return textMatcher{}, false
		case len(set) > 0:
			c.add(field, "present cannot be combined with "+set[0].name, "use one test")
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
		// The time a search takes grows with the size of the compiled
		// expression, so that size has a limit, per expression and in sum.
		size := 0
		if parsed, perr := syntax.Parse(pattern, syntax.Perl); perr == nil {
			if prog, cerr := syntax.Compile(parsed.Simplify()); cerr == nil {
				size = len(prog.Inst)
			}
		}
		if size > MaxRegexSize {
			c.add(sub, fmt.Sprintf("the regular expression is too large: %d steps when compiled; the limit is %d", size, MaxRegexSize),
				"repeats such as {50} multiply; shorten them, or use contains, prefix or suffix")
			return textMatcher{}, false
		}
		if c.regexSize += size; c.regexSize > MaxRegexTotal && c.regexSize-size <= MaxRegexTotal {
			c.add(sub, fmt.Sprintf("the regular expressions of the rule set are too large together: more than %d steps", MaxRegexTotal),
				"use contains, prefix, suffix or equals where plain text is meant; they cost almost nothing")
		}
		tm.re = re
		// A value too long to search: see MaxRegexInput.
		tm.tooLong = c.favours == c.negated
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
