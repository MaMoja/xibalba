package rules

import (
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

func yes() *bool { b := true; return &b }
func no() *bool  { b := false; return &b }

func contains(s string) *StringSpec { return &StringSpec{Contains: s} }
func prefix(s string) *StringSpec   { return &StringSpec{Prefix: s} }

// request builds a Request the way internal/gate does.
func request(method, host, path, ua, client string, headers ...string) *Request {
	h := http.Header{}
	for i := 0; i+1 < len(headers); i += 2 {
		h.Add(headers[i], headers[i+1])
	}
	r := &Request{Method: method, Host: NormalizeHost(host), Path: NormalizePath(path), UserAgent: ua, Header: h}
	if client != "" {
		r.Client = netip.MustParseAddr(client)
	}
	return r
}

func mustCompile(t testing.TB, spec Spec) *Engine {
	t.Helper()
	e, problems := Compile(spec)
	if len(problems) > 0 {
		t.Fatalf("Compile reported problems: %+v", problems)
	}
	return e
}

// one compiles a single rule and reports whether it matches req.
func matches(t *testing.T, m MatchSpec, req *Request) bool {
	t.Helper()
	e := mustCompile(t, Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "r", Match: m, Action: Deny}}})
	return e.Evaluate(req).Action == Deny
}

func TestConditions(t *testing.T) {
	browser := "Mozilla/5.0 (X11; Linux x86_64) Firefox/130.0"
	tests := []struct {
		name  string
		match MatchSpec
		req   *Request
		want  bool
	}{
		// Text tests, case-insensitive by default.
		{"contains", MatchSpec{UserAgent: contains("ExampleBot")}, request("GET", "h", "/", "Mozilla/5.0 (compatible; ExampleBot/1.0)", ""), true},
		{"contains ignores case", MatchSpec{UserAgent: contains("ExampleBot")}, request("GET", "h", "/", "examplebot", ""), true},
		{"contains, pattern in other case", MatchSpec{UserAgent: contains("EXAMPLEBOT")}, request("GET", "h", "/", "ExampleBot/1.0", ""), true},
		{"contains misses", MatchSpec{UserAgent: contains("ExampleBot")}, request("GET", "h", "/", browser, ""), false},
		{"contains on empty text", MatchSpec{UserAgent: contains("ExampleBot")}, request("GET", "h", "/", "", ""), false},
		{"case sensitive contains", MatchSpec{UserAgent: &StringSpec{Contains: "ExampleBot", CaseSensitive: true}}, request("GET", "h", "/", "examplebot", ""), false},
		{"equals", MatchSpec{UserAgent: &StringSpec{Equals: "curl/8.0"}}, request("GET", "h", "/", "CURL/8.0", ""), true},
		{"equals needs the whole text", MatchSpec{UserAgent: &StringSpec{Equals: "curl"}}, request("GET", "h", "/", "curl/8.0", ""), false},
		{"prefix", MatchSpec{Path: prefix("/Admin")}, request("GET", "h", "/admin/users", browser, ""), true},
		{"prefix longer than text", MatchSpec{Path: prefix("/administrator")}, request("GET", "h", "/admin", browser, ""), false},
		{"suffix", MatchSpec{Path: &StringSpec{Suffix: ".PHP"}}, request("GET", "h", "/index.php", browser, ""), true},
		{"suffix longer than text", MatchSpec{Path: &StringSpec{Suffix: "/index.php"}}, request("GET", "h", "/x", browser, ""), false},
		{"regex", MatchSpec{Path: &StringSpec{Regex: `^/wiki/.+\.(pdf|zip)$`}}, request("GET", "h", "/Wiki/Handbuch.PDF", browser, ""), true},
		{"regex case sensitive", MatchSpec{Path: &StringSpec{Regex: `^/wiki/`, CaseSensitive: true}}, request("GET", "h", "/Wiki/x", browser, ""), false},
		{"non-ASCII text must be equal", MatchSpec{Path: contains("/straße")}, request("GET", "h", "/Straße/1", browser, ""), true},

		// Method.
		{"method in list", MatchSpec{Method: []string{"post", "PUT"}}, request("POST", "h", "/", browser, ""), true},
		{"method not in list", MatchSpec{Method: []string{"POST"}}, request("GET", "h", "/", browser, ""), false},

		// Host: case, port and trailing dot never matter.
		{"host", MatchSpec{Host: &StringSpec{Equals: "Portal.Example.org"}}, request("GET", "portal.EXAMPLE.org:8443", "/", browser, ""), true},
		{"host trailing dot", MatchSpec{Host: &StringSpec{Equals: "example.org"}}, request("GET", "example.org.", "/", browser, ""), true},
		{"host suffix", MatchSpec{Host: &StringSpec{Suffix: ".example.org"}}, request("GET", "a.example.org", "/", browser, ""), true},
		{"other host", MatchSpec{Host: &StringSpec{Equals: "example.org"}}, request("GET", "example.org.evil.test", "/", browser, ""), false},

		// Headers.
		{"header present", MatchSpec{Header: map[string]*StringSpec{"accept-language": {Present: yes()}}}, request("GET", "h", "/", browser, "", "Accept-Language", "de"), true},
		{"header present but missing", MatchSpec{Header: map[string]*StringSpec{"Accept-Language": {Present: yes()}}}, request("GET", "h", "/", browser, ""), false},
		{"header absent", MatchSpec{Header: map[string]*StringSpec{"Accept-Language": {Present: no()}}}, request("GET", "h", "/", browser, ""), true},
		{"header absent but there", MatchSpec{Header: map[string]*StringSpec{"Accept-Language": {Present: no()}}}, request("GET", "h", "/", browser, "", "Accept-Language", "de"), false},
		{"header value", MatchSpec{Header: map[string]*StringSpec{"Accept": contains("text/html")}}, request("GET", "h", "/", browser, "", "Accept", "TEXT/HTML,*/*"), true},
		{"header value test on missing header", MatchSpec{Header: map[string]*StringSpec{"Accept": contains("text/html")}}, request("GET", "h", "/", browser, ""), false},
		{"any of several header values", MatchSpec{Header: map[string]*StringSpec{"Via": contains("proxy-b")}}, request("GET", "h", "/", browser, "", "Via", "1.1 proxy-a", "Via", "1.1 proxy-b"), true},
		{"all headers must hold", MatchSpec{Header: map[string]*StringSpec{"Accept": contains("html"), "Accept-Language": {Present: yes()}}}, request("GET", "h", "/", browser, "", "Accept", "text/html"), false},

		// Client address.
		{"ip in network", MatchSpec{IP: []string{"198.51.100.0/24"}}, request("GET", "h", "/", browser, "198.51.100.77"), true},
		{"ip outside network", MatchSpec{IP: []string{"198.51.100.0/24"}}, request("GET", "h", "/", browser, "198.51.101.1"), false},
		{"single ip", MatchSpec{IP: []string{"2001:db8::1"}}, request("GET", "h", "/", browser, "2001:db8::1"), true},
		{"ipv6 network", MatchSpec{IP: []string{"2001:db8::/32"}}, request("GET", "h", "/", browser, "2001:db8:1::5"), true},
		{"ipv4-mapped rule matches ipv4 client", MatchSpec{IP: []string{"::ffff:198.51.100.0/120"}}, request("GET", "h", "/", browser, "198.51.100.9"), true},
		{"unknown client matches no ip rule", MatchSpec{IP: []string{"0.0.0.0/0", "::/0"}}, request("GET", "h", "/", browser, ""), false},

		// Combinations.
		{"conditions are ANDed", MatchSpec{UserAgent: contains("bot"), Path: prefix("/api")}, request("GET", "h", "/api/x", "somebot", ""), true},
		{"conditions are ANDed, one fails", MatchSpec{UserAgent: contains("bot"), Path: prefix("/api")}, request("GET", "h", "/shop", "somebot", ""), false},
		{"any", MatchSpec{Any: []MatchSpec{{UserAgent: contains("alpha")}, {UserAgent: contains("beta")}}}, request("GET", "h", "/", "BetaFetcher", ""), true},
		{"any, none holds", MatchSpec{Any: []MatchSpec{{UserAgent: contains("alpha")}, {UserAgent: contains("beta")}}}, request("GET", "h", "/", browser, ""), false},
		{"all", MatchSpec{All: []MatchSpec{{UserAgent: contains("mozilla")}, {Path: prefix("/")}}}, request("GET", "h", "/", browser, ""), true},
		{"not", MatchSpec{Not: &MatchSpec{UserAgent: contains("mozilla")}}, request("GET", "h", "/", "curl/8", ""), true},
		{"not, inner holds", MatchSpec{Not: &MatchSpec{UserAgent: contains("mozilla")}}, request("GET", "h", "/", browser, ""), false},
		{"claims a name but comes from elsewhere",
			MatchSpec{UserAgent: contains("ExampleBot"), Not: &MatchSpec{IP: []string{"192.0.2.0/24"}}},
			request("GET", "h", "/", "ExampleBot/1.0", "203.0.113.5"), true},
		{"claims a name and comes from the right network",
			MatchSpec{UserAgent: contains("ExampleBot"), Not: &MatchSpec{IP: []string{"192.0.2.0/24"}}},
			request("GET", "h", "/", "ExampleBot/1.0", "192.0.2.44"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matches(t, tt.match, tt.req); got != tt.want {
				t.Errorf("match = %v, want %v", got, tt.want)
			}
		})
	}
}

// A rule on a path must hold however the path is spelled.
func TestPathSpellingsCannotDodgeARule(t *testing.T) {
	rule := MatchSpec{Path: prefix("/admin")}
	for _, raw := range []string{
		"/admin", "/admin/", "/admin/users", "/ADMIN",
		"//admin", "///admin/users", "/./admin", "/x/../admin", "/x/y/../../admin/",
		"/admin/../admin", `\admin`, `/\admin`, `/x\..\admin`, "admin", "/a/./../admin",
		"/admin;jsessionid=1", "/public/..;/admin", "/;x/admin", "/admin;/users", "/x;a=b/..;c/admin/",
	} {
		if !matches(t, rule, request("GET", "h", raw, "x", "")) {
			t.Errorf("path %q (normalised to %q) dodged the rule", raw, NormalizePath(raw))
		}
	}
	for _, raw := range []string{"/", "/public", "/admin/..", "/admi", "/x/admin"} {
		if matches(t, rule, request("GET", "h", raw, "x", "")) {
			t.Errorf("path %q (normalised to %q) matched but should not", raw, NormalizePath(raw))
		}
	}
}

func TestNormalizePath(t *testing.T) {
	tests := map[string]string{
		"":             "/",
		"/":            "/",
		"/a":           "/a",
		"/a/":          "/a/",
		"//":           "/",
		"/a//b":        "/a/b",
		"/a/./b/":      "/a/b/",
		"/a/../b":      "/b",
		"/../../a":     "/a",
		"/a/..":        "/",
		"/a/.":         "/a",
		"/..":          "/",
		"/.hidden":     "/.hidden",
		"/a..b/..c":    "/a..b/..c",
		`\a\b`:         "/a/b",
		"*":            "/*",
		"/a/b/../../c": "/c",
		"/a;x=1/b;y":   "/a/b",
		"/a/..;/b":     "/b",
		";x":           "/",
		"/a;":          "/a",
	}
	for in, want := range tests {
		if got := NormalizePath(in); got != want {
			t.Errorf("NormalizePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeHost(t *testing.T) {
	tests := map[string]string{
		"example.org":       "example.org",
		"Example.ORG":       "example.org",
		"example.org:8080":  "example.org",
		"example.org.":      "example.org",
		"EXAMPLE.org.:443":  "example.org",
		"[2001:db8::1]:443": "2001:db8::1",
		"[2001:DB8::1]":     "2001:db8::1",
		"2001:db8::1":       "2001:db8::1",
		"":                  "",
	}
	for in, want := range tests {
		if got := NormalizeHost(in); got != want {
			t.Errorf("NormalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEvaluationOrder(t *testing.T) {
	spec := Spec{
		DefaultAction: Allow,
		Thresholds:    []ThresholdSpec{{Weight: 10, Action: Challenge}, {Weight: 20, Action: Deny}},
		Rules: []RuleSpec{
			{Name: "office", Match: MatchSpec{IP: []string{"192.0.2.0/24"}}, Action: Allow},
			{Name: "block-bad", Match: MatchSpec{UserAgent: contains("BadBot")}, Action: Deny},
			{Name: "no-language", Match: MatchSpec{Header: map[string]*StringSpec{"Accept-Language": {Present: no()}}}, Action: Weigh, Weight: 10},
			{Name: "scripted", Match: MatchSpec{UserAgent: contains("python")}, Action: Weigh, Weight: 10},
			{Name: "browser", Match: MatchSpec{UserAgent: contains("Mozilla")}, Action: Weigh, Weight: -5},
			{Name: "login", Match: MatchSpec{Path: prefix("/login")}, Action: Challenge},
		},
	}
	e := mustCompile(t, spec)

	tests := []struct {
		name       string
		req        *Request
		wantAction Action
		wantSource string
		wantWeight int
	}{
		{"first deciding rule wins", request("GET", "h", "/", "BadBot", "192.0.2.9"), Allow, "rule:office", 0},
		{"later deny", request("GET", "h", "/", "BadBot", "203.0.113.1", "Accept-Language", "de"), Deny, "rule:block-bad", 0},
		{"nothing matches: default", request("GET", "h", "/", "curl/8", "203.0.113.1", "Accept-Language", "de"), Allow, "default", 0},
		{"weights below every threshold", request("GET", "h", "/", "Mozilla", "203.0.113.1"), Allow, "default", 5},
		{"weights reach the first threshold", request("GET", "h", "/", "curl/8", "203.0.113.1"), Challenge, "threshold:10", 10},
		{"weights reach the highest threshold", request("GET", "h", "/", "python-urllib", "203.0.113.1"), Deny, "threshold:20", 20},
		{"negative weight pulls back under", request("GET", "h", "/", "Mozilla python", "203.0.113.1"), Challenge, "threshold:10", 15},
		{"deciding rule after weigh rules beats thresholds", request("GET", "h", "/login", "python-urllib", "203.0.113.1"), Challenge, "rule:login", 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := e.Evaluate(tt.req)
			src := e.Sources()[d.Source]
			if d.Action != tt.wantAction || src.ID != tt.wantSource || d.Weight != tt.wantWeight {
				t.Errorf("got %s by %s at weight %d, want %s by %s at weight %d",
					d.Action, src.ID, d.Weight, tt.wantAction, tt.wantSource, tt.wantWeight)
			}
			if src.Action != d.Action {
				t.Errorf("source %s is recorded with action %s but decided %s", src.ID, src.Action, d.Action)
			}
		})
	}
}

func TestThresholdsMayBeListedInAnyOrder(t *testing.T) {
	e := mustCompile(t, Spec{
		DefaultAction: Allow,
		Thresholds:    []ThresholdSpec{{Weight: 30, Action: Deny}, {Weight: 10, Action: Challenge}},
		Rules:         []RuleSpec{{Name: "w", Match: MatchSpec{Path: prefix("/")}, Action: Weigh, Weight: 15}},
	})
	if d := e.Evaluate(request("GET", "h", "/", "x", "")); d.Action != Challenge {
		t.Errorf("at weight 15 got %s, want challenge", d.Action)
	}
}

func TestEmptyRuleSetUsesTheDefault(t *testing.T) {
	for _, def := range []Action{Allow, Deny, Challenge} {
		e := mustCompile(t, Spec{DefaultAction: def})
		d := e.Evaluate(request("GET", "h", "/", "x", ""))
		if d.Action != def || e.Sources()[d.Source].ID != "default" {
			t.Errorf("default %s: got %s by %s", def, d.Action, e.Sources()[d.Source].ID)
		}
	}
}

func TestSourcesAndReferences(t *testing.T) {
	e := mustCompile(t, Spec{
		DefaultAction: Allow,
		Thresholds:    []ThresholdSpec{{Weight: 10, Action: Challenge}},
		Rules: []RuleSpec{
			{Name: "a", Match: MatchSpec{Path: prefix("/a")}, Action: Deny},
			{Name: "w", Match: MatchSpec{Path: prefix("/w")}, Action: Weigh, Weight: 1},
			{Name: "b", Match: MatchSpec{Path: prefix("/b")}, Action: Allow},
		},
	})
	var ids []string
	refs := map[string]bool{}
	for _, s := range e.Sources() {
		ids = append(ids, s.ID)
		if len(s.Reference) != 8 || refs[s.Reference] {
			t.Errorf("reference %q of %s is not a unique 8-character code", s.Reference, s.ID)
		}
		refs[s.Reference] = true
	}
	if got, want := strings.Join(ids, " "), "rule:a rule:b threshold:10 default"; got != want {
		t.Errorf("sources = %q, want %q (weigh rules decide nothing and are not sources)", got, want)
	}

	// The reference depends only on the name, so it survives reordering and restarts.
	again := mustCompile(t, Spec{DefaultAction: Deny, Rules: []RuleSpec{{Name: "a", Match: MatchSpec{Path: prefix("/zzz")}, Action: Deny}}})
	if again.Sources()[0].Reference != e.Sources()[0].Reference {
		t.Error("the reference of rule a changed between rule sets")
	}

	if !e.Uses(Challenge) || !e.Uses(Deny) || e.Len() != 3 {
		t.Errorf("Uses/Len wrong: challenge %v deny %v len %d", e.Uses(Challenge), e.Uses(Deny), e.Len())
	}
}

func TestCompileProblems(t *testing.T) {
	ok := MatchSpec{Path: prefix("/x")}
	long := strings.Repeat("a", MaxPatternLength+1)

	deep := MatchSpec{Path: prefix("/x")}
	for i := 0; i < MaxDepth; i++ {
		inner := deep
		deep = MatchSpec{Not: &inner}
	}

	tests := []struct {
		name      string
		spec      Spec
		wantRule  int
		wantField string
		wantText  string
	}{
		{"bad default", Spec{DefaultAction: "weigh"}, -1, "default_action", "not an action a default can take"},
		{"missing default", Spec{}, -1, "default_action", "not an action"},
		{"no name", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Match: ok, Action: Deny}}}, 0, "name", "no name"},
		{"bad name", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "Block Bots!", Match: ok, Action: Deny}}}, 0, "name", "not a valid rule name"},
		{"duplicate name", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "a", Match: ok, Action: Deny}, {Name: "a", Match: ok, Action: Allow}}}, 1, "name", "already used by rule number 1"},
		{"no action", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "a", Match: ok}}}, 0, "action", "no action"},
		{"unknown action", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "a", Match: ok, Action: "block"}}}, 0, "action", `"block" is not an action`},
		{"weigh without weight", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "a", Match: ok, Action: Weigh}}}, 0, "weight", "needs a weight"},
		{"weight out of range", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "a", Match: ok, Action: Weigh, Weight: 5000}}}, 0, "weight", "out of range"},
		{"weight on a deciding rule", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "a", Match: ok, Action: Deny, Weight: 5}}}, 0, "weight", "only used with action weigh"},
		{"no conditions", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "a", Action: Deny}}}, 0, "match", "would match every request"},
		{"no test", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "a", Match: MatchSpec{UserAgent: &StringSpec{}}, Action: Deny}}}, 0, "match.user_agent", "no test"},
		{"two tests", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "a", Match: MatchSpec{UserAgent: &StringSpec{Contains: "a", Prefix: "b"}}, Action: Deny}}}, 0, "match.user_agent", "several tests"},
		{"bad regex", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "a", Match: MatchSpec{Path: &StringSpec{Regex: "(unclosed"}}, Action: Deny}}}, 0, "match.path.regex", "not valid"},
		{"lookahead is not RE2", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "a", Match: MatchSpec{Path: &StringSpec{Regex: "(?=x)"}}, Action: Deny}}}, 0, "match.path.regex", "not valid"},
		{"pattern too long", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "a", Match: MatchSpec{UserAgent: contains(long)}, Action: Deny}}}, 0, "match.user_agent.contains", "limit"},
		{"present outside header", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "a", Match: MatchSpec{Path: &StringSpec{Present: yes()}}, Action: Deny}}}, 0, "match.path.present", "only be used with a header"},
		{"present with another test", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "a", Match: MatchSpec{Header: map[string]*StringSpec{"Accept": {Present: yes(), Contains: "x"}}}, Action: Deny}}}, 0, "match.header.Accept", "cannot be combined"},
		{"case sensitive host", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "a", Match: MatchSpec{Host: &StringSpec{Equals: "x", CaseSensitive: true}}, Action: Deny}}}, 0, "match.host.case_sensitive", "never case sensitive"},
		{"bad header name", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "a", Match: MatchSpec{Header: map[string]*StringSpec{"Bad Name": {Present: yes()}}}, Action: Deny}}}, 0, "match.header.Bad Name", "not a header name"},
		{"header without test", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "a", Match: MatchSpec{Header: map[string]*StringSpec{"Accept": nil}}, Action: Deny}}}, 0, "match.header.Accept", "no test"},
		{"rule on a forwarding header", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "a", Match: MatchSpec{Header: map[string]*StringSpec{"x-forwarded-for": contains("10.")}}, Action: Allow}}}, 0, "match.header.x-forwarded-for", "cannot be trusted"},
		{"rule on x-real-ip", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "a", Match: MatchSpec{Header: map[string]*StringSpec{"X-Real-IP": contains("10.")}}, Action: Allow}}}, 0, "match.header.X-Real-IP", "cannot be trusted"},
		{"user agent as header", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "a", Match: MatchSpec{Header: map[string]*StringSpec{"User-Agent": contains("x")}}, Action: Deny}}}, 0, "match.header.User-Agent", "its own condition"},
		{"bad method", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "a", Match: MatchSpec{Method: []string{"GET", "P OST"}}, Action: Deny}}}, 0, "match.method[1]", "not an HTTP method"},
		{"empty method list", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "a", Match: MatchSpec{Method: []string{}}, Action: Deny}}}, 0, "match.method", "needs 1 to"},
		{"bad ip", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "a", Match: MatchSpec{IP: []string{"10.0.0.0/8", "300.1.1.1"}}, Action: Deny}}}, 0, "match.ip[1]", "not an IP address or network"},
		{"empty any", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "a", Match: MatchSpec{Any: []MatchSpec{}}, Action: Deny}}}, 0, "match.any", "needs 1 to"},
		{"empty group inside any", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "a", Match: MatchSpec{Any: []MatchSpec{ok, {}}}, Action: Deny}}}, 0, "match.any[1]", "no conditions"},
		{"nested too deep", Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "a", Match: deep, Action: Deny}}}, 0, "match" + strings.Repeat(".not", MaxDepth), "nested more than"},
		{"threshold weight zero", Spec{DefaultAction: Allow, Thresholds: []ThresholdSpec{{Weight: 0, Action: Deny}}}, -1, "thresholds[0].weight", "out of range"},
		{"threshold allow", Spec{DefaultAction: Allow, Thresholds: []ThresholdSpec{{Weight: 5, Action: Allow}}}, -1, "thresholds[0].action", "not an action a threshold can take"},
		{"duplicate threshold", Spec{DefaultAction: Allow, Thresholds: []ThresholdSpec{{Weight: 5, Action: Deny}, {Weight: 5, Action: Challenge}}}, -1, "thresholds[1].weight", "already used by threshold number 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, problems := Compile(tt.spec)
			if e != nil {
				t.Error("Compile returned an engine for an invalid rule set")
			}
			for _, p := range problems {
				if p.Rule == tt.wantRule && p.Field == tt.wantField && strings.Contains(p.Message, tt.wantText) {
					if p.Hint == "" {
						t.Errorf("problem has no hint: %+v", p)
					}
					return
				}
			}
			t.Errorf("no problem at rule %d field %q containing %q; got %+v", tt.wantRule, tt.wantField, tt.wantText, problems)
		})
	}
}

func TestCompileReportsEveryProblem(t *testing.T) {
	_, problems := Compile(Spec{
		DefaultAction: "nonsense",
		Thresholds:    []ThresholdSpec{{Weight: -1, Action: Allow}},
		Rules: []RuleSpec{
			{Name: "", Action: "x"},
			{Name: "ok", Match: MatchSpec{Path: &StringSpec{Regex: "("}, IP: []string{"nope"}}, Action: Deny},
		},
	})
	if len(problems) < 8 {
		t.Errorf("got %d problems, want all of them in one pass: %+v", len(problems), problems)
	}
}

// One mistake must produce one problem. In particular a rule whose only
// condition is invalid must not also be reported as having no conditions.
func TestOneMistakeOneProblem(t *testing.T) {
	tests := map[string]MatchSpec{
		"bad regex":             {UserAgent: &StringSpec{Regex: "(GPT|Claude"}},
		"forwarding header":     {Header: map[string]*StringSpec{"X-Forwarded-For": {Prefix: "10."}}},
		"bad address":           {IP: []string{"nonsense"}},
		"bad method":            {Method: []string{"NOT A METHOD"}},
		"bad condition in not":  {Not: &MatchSpec{Path: &StringSpec{}}},
		"bad condition in any":  {Any: []MatchSpec{{Path: &StringSpec{Regex: "("}}}},
		"two tests on one text": {Path: &StringSpec{Prefix: "/a", Suffix: "b"}},
	}
	for name, match := range tests {
		t.Run(name, func(t *testing.T) {
			_, problems := Compile(Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "r", Match: match, Action: Deny}}})
			if len(problems) != 1 {
				t.Errorf("got %d problems, want exactly 1: %+v", len(problems), problems)
			}
		})
	}
}

func TestRegexErrorQuotesWhatWasWritten(t *testing.T) {
	_, problems := Compile(Spec{DefaultAction: Allow, Rules: []RuleSpec{
		{Name: "r", Match: MatchSpec{UserAgent: &StringSpec{Regex: "(GPT|Claude"}}, Action: Deny},
	}})
	if len(problems) != 1 || !strings.Contains(problems[0].Message, "`(GPT|Claude`") || strings.Contains(problems[0].Message, "(?i)") {
		t.Errorf("problems = %+v, want the expression quoted as written", problems)
	}
}

func TestTooManyRules(t *testing.T) {
	spec := Spec{DefaultAction: Allow, Rules: make([]RuleSpec, MaxRules+1)}
	_, problems := Compile(spec)
	if len(problems) != 1 || !strings.Contains(problems[0].Message, "limit") {
		t.Errorf("got %+v, want one problem about the limit", problems)
	}
}

// Text chosen to be slow for backtracking regular expression engines must
// not be slow here.
func TestHostileInputIsCheap(t *testing.T) {
	e := mustCompile(t, Spec{DefaultAction: Allow, Rules: []RuleSpec{
		{Name: "re", Match: MatchSpec{UserAgent: &StringSpec{Regex: `(a+)+$`}}, Action: Deny},
		{Name: "sub", Match: MatchSpec{UserAgent: contains(strings.Repeat("a", 200) + "b")}, Action: Deny},
	}})
	req := request("GET", "h", "/"+strings.Repeat("../", 5000), strings.Repeat("a", 60000)+"!", "")
	for i := 0; i < 20; i++ {
		e.Evaluate(req)
	}
	// No assertion on time: the test fails by timing out if the work is not bounded.
}

func TestEvaluateDoesNotAllocate(t *testing.T) {
	e := benchEngine(t)
	req := request("GET", "www.example.org", "/shop/category/42", "Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0",
		"203.0.113.50", "Accept", "text/html", "Accept-Language", "de-DE")
	if allocs := testing.AllocsPerRun(200, func() { e.Evaluate(req) }); allocs != 0 {
		t.Errorf("Evaluate allocated %.0f times per request, want 0", allocs)
	}
}

func TestEngineIsSafeForConcurrentUse(t *testing.T) {
	e := benchEngine(t)
	req := request("GET", "h", "/", "Mozilla", "203.0.113.50")
	done := make(chan struct{})
	for g := 0; g < 8; g++ {
		go func() {
			for i := 0; i < 2000; i++ {
				e.Evaluate(req)
			}
			done <- struct{}{}
		}()
	}
	for g := 0; g < 8; g++ {
		<-done
	}
}

// benchEngine builds a rule set of realistic size: 100 user-agent rules,
// 20 network rules, a few path and header rules.
func benchEngine(t testing.TB) *Engine {
	spec := Spec{DefaultAction: Allow, Thresholds: []ThresholdSpec{{Weight: 10, Action: Challenge}, {Weight: 30, Action: Deny}}}
	for i := 0; i < 100; i++ {
		spec.Rules = append(spec.Rules, RuleSpec{
			Name: fmt.Sprintf("bot-%d", i), Match: MatchSpec{UserAgent: contains(fmt.Sprintf("ExampleBot%d/", i))}, Action: Deny,
		})
	}
	for i := 0; i < 20; i++ {
		spec.Rules = append(spec.Rules, RuleSpec{
			Name: fmt.Sprintf("net-%d", i), Match: MatchSpec{IP: []string{fmt.Sprintf("10.%d.0.0/16", i), fmt.Sprintf("2001:db8:%x::/48", i)}}, Action: Deny,
		})
	}
	spec.Rules = append(spec.Rules,
		RuleSpec{Name: "no-language", Match: MatchSpec{Header: map[string]*StringSpec{"Accept-Language": {Present: no()}}}, Action: Weigh, Weight: 5},
		RuleSpec{Name: "archive", Match: MatchSpec{Path: &StringSpec{Regex: `\.(zip|tar|gz|iso)$`}}, Action: Challenge},
		RuleSpec{Name: "api", Match: MatchSpec{Path: prefix("/api/"), Method: []string{"POST", "PUT", "DELETE"}}, Action: Weigh, Weight: 5},
	)
	return mustCompile(t, spec)
}

// BenchmarkEvaluate measures the worst ordinary case: a browser request that
// matches no rule and so is tested against all of them.
func BenchmarkEvaluate(b *testing.B) {
	e := benchEngine(b)
	req := request("GET", "www.example.org", "/shop/category/42", "Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0",
		"203.0.113.50", "Accept", "text/html", "Accept-Language", "de-DE")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.Evaluate(req)
	}
}

// The cost of a rule set has bounds that hold whatever the author writes.
func TestLimitsOnWhatARuleSetMayCost(t *testing.T) {
	rule := func(name, regex string, action Action) RuleSpec {
		return RuleSpec{Name: name, Match: MatchSpec{Path: &StringSpec{Regex: regex}}, Action: action}
	}
	for name, tt := range map[string]struct {
		rules []RuleSpec
		want  string
	}{
		"one huge expression": {[]RuleSpec{rule("a", "([a-z]{0,20}){15}Q", Deny)}, "too large"},
		"nested repeats":      {[]RuleSpec{rule("a", "(x{30}){30}", Deny)}, "too large"},
		"many middling ones": {func() (out []RuleSpec) {
			for i := 0; i < 40; i++ {
				out = append(out, rule(fmt.Sprintf("r%d", i), fmt.Sprintf("a{150}%d", i%10), Deny))
			}
			return out
		}(), "too large together"},
	} {
		_, problems := Compile(Spec{DefaultAction: Allow, Rules: tt.rules})
		found := 0
		for _, p := range problems {
			if strings.Contains(p.Message, tt.want) {
				found++
			}
		}
		if found != 1 {
			t.Errorf("%s: %d problems say %q: %+v", name, found, tt.want, problems)
		}
	}

	// Nesting multiplies: ten times ten times ten ... groups.
	leaf := MatchSpec{Path: &StringSpec{Prefix: "/x"}}
	wide := leaf
	for depth := 0; depth < 5; depth++ {
		list := make([]MatchSpec, 10)
		for i := range list {
			list[i] = wide
		}
		wide = MatchSpec{Any: list}
	}
	_, problems := Compile(Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "wide", Match: wide, Action: Deny}}})
	if len(problems) != 1 || !strings.Contains(problems[0].Message, "groups of conditions") {
		t.Errorf("a rule of 100000 groups: %d problems, first %+v", len(problems), problems[:min(len(problems), 1)])
	}
}

// A value too long to search counts against the request, never for it.
func TestRegexOnAValueTooLongToSearch(t *testing.T) {
	long := "/" + strings.Repeat("a", MaxRegexInput) + ".php"
	e, problems := Compile(Spec{DefaultAction: Challenge, Rules: []RuleSpec{
		{Name: "let-static", Match: MatchSpec{Path: &StringSpec{Regex: `a+\.php$`}}, Action: Allow},
	}})
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	if d := e.Evaluate(&Request{Method: "GET", Path: long}); d.Action != Challenge {
		t.Errorf("an allow rule matched a value it did not search: %s", d.Action)
	}
	e, _ = Compile(Spec{DefaultAction: Allow, Rules: []RuleSpec{
		{Name: "no-php", Match: MatchSpec{Path: &StringSpec{Regex: `\.php$`}}, Action: Deny},
		{Name: "only-shop", Match: MatchSpec{Not: &MatchSpec{Path: &StringSpec{Regex: `^/shop`}}}, Action: Deny},
	}})
	if d := e.Evaluate(&Request{Method: "GET", Path: "/shop/" + strings.Repeat("a", MaxRegexInput)}); d.Action != Deny {
		t.Errorf("a long value slipped past the deny rules: %s", d.Action)
	}
	if d := e.Evaluate(&Request{Method: "GET", Path: "/shop/a"}); d.Action != Allow {
		t.Errorf("a short value: %s", d.Action)
	}
}

func BenchmarkWorstRegexRuleSet(b *testing.B) {
	var set []RuleSpec
	for i := 0; i < 4; i++ { // as much expression as a rule set may hold
		set = append(set, RuleSpec{Name: fmt.Sprintf("r%d", i), Match: MatchSpec{UserAgent: &StringSpec{Regex: fmt.Sprintf("(.*a){9}.{0,170}%dQ", i)}}, Action: Deny})
	}
	e, problems := Compile(Spec{DefaultAction: Allow, Rules: set})
	if len(problems) != 0 {
		b.Fatal(problems)
	}
	req := &Request{Method: "GET", Path: "/", UserAgent: strings.Repeat("a", MaxRegexInput)}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.Evaluate(req)
	}
}
