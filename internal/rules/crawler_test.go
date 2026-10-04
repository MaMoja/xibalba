package rules

import (
	"strings"
	"testing"
)

var testCatalog = &Catalog{
	Classes: []string{"training", "ai-search", "user-fetch", "search-engine", "archive", "other"},
	Names:   []string{"GPTBot", "OAI-SearchBot", "Googlebot"},
}

func crawlerRequest(name, class string, status CrawlerStatus) *Request {
	r := request("GET", "h", "/", name+"/1.0", "192.0.2.1")
	r.Crawler = Crawler{Name: name, Class: class, Status: status}
	return r
}

func TestCrawlerCondition(t *testing.T) {
	var (
		genuine  = crawlerRequest("GPTBot", "training", CrawlerVerified)
		impostor = crawlerRequest("GPTBot", "training", CrawlerImpostor)
		unknown  = crawlerRequest("GPTBot", "training", CrawlerUnknown)
		search   = crawlerRequest("Googlebot", "search-engine", CrawlerVerified)
		person   = request("GET", "h", "/", "Mozilla/5.0 Firefox/130.0", "192.0.2.1")
	)
	tests := []struct {
		name  string
		spec  CrawlerSpec
		match []*Request
		miss  []*Request
	}{
		{"class, claim alone", CrawlerSpec{Class: []string{"training"}},
			[]*Request{genuine, impostor, unknown}, []*Request{search, person}},
		{"several classes", CrawlerSpec{Class: []string{"archive", "search-engine"}},
			[]*Request{search}, []*Request{genuine, person}},
		{"name, written in another case", CrawlerSpec{Name: []string{"gptbot"}},
			[]*Request{genuine, impostor, unknown}, []*Request{search, person}},
		{"verified", CrawlerSpec{Verified: yes()},
			[]*Request{genuine, search}, []*Request{impostor, unknown, person}},
		// "not verified" means refuted. A crawler that cannot be checked,
		// or is not checked yet, is not an impostor.
		{"impostor", CrawlerSpec{Verified: no()},
			[]*Request{impostor}, []*Request{genuine, unknown, search, person}},
		{"class and verified", CrawlerSpec{Class: []string{"training"}, Verified: yes()},
			[]*Request{genuine}, []*Request{impostor, unknown, search, person}},
		{"class and name must both hold", CrawlerSpec{Class: []string{"search-engine"}, Name: []string{"GPTBot"}},
			nil, []*Request{genuine, search, person}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := tt.spec
			e := mustCompile(t, Spec{DefaultAction: Allow, Crawlers: testCatalog,
				Rules: []RuleSpec{{Name: "r", Match: MatchSpec{Crawler: &spec}, Action: Deny}}})
			if !e.UsesCrawlers() {
				t.Error("UsesCrawlers() = false")
			}
			for _, r := range tt.match {
				if e.Evaluate(r).Action != Deny {
					t.Errorf("did not match %+v", r.Crawler)
				}
			}
			for _, r := range tt.miss {
				if e.Evaluate(r).Action == Deny {
					t.Errorf("matched %+v", r.Crawler)
				}
			}
		})
	}
}

func TestEngineWithoutCrawlerConditions(t *testing.T) {
	e := mustCompile(t, Spec{DefaultAction: Allow, Crawlers: testCatalog,
		Rules: []RuleSpec{{Name: "r", Match: MatchSpec{UserAgent: contains("x")}, Action: Deny}}})
	if e.UsesCrawlers() {
		t.Error("UsesCrawlers() = true for a rule set without crawler conditions")
	}
}

// Anyone can send a crawler's name. A rule must not be able to let a request
// through, or make it look better, because of the name alone.
func TestACrawlerIsOnlyFavouredWhenVerified(t *testing.T) {
	named := MatchSpec{Crawler: &CrawlerSpec{Name: []string{"Googlebot"}}}
	impostors := MatchSpec{Crawler: &CrawlerSpec{Name: []string{"Googlebot"}, Verified: no()}}
	verified := MatchSpec{Crawler: &CrawlerSpec{Name: []string{"Googlebot"}, Verified: yes()}}

	refused := []struct {
		name string
		rule RuleSpec
		at   string
	}{
		{"allow by name", RuleSpec{Match: named, Action: Allow}, "match.crawler.verified"},
		{"allow impostors", RuleSpec{Match: impostors, Action: Allow}, "match.crawler.verified"},
		{"lower the score by name", RuleSpec{Match: named, Action: Weigh, Weight: -5}, "match.crawler.verified"},
		{"hidden in any", RuleSpec{Match: MatchSpec{Any: []MatchSpec{{Path: prefix("/x")}, named}}, Action: Allow}, "match.any[1].crawler.verified"},
		{"hidden in all", RuleSpec{Match: MatchSpec{All: []MatchSpec{named}}, Action: Allow}, "match.all[0].crawler.verified"},
		{"deny everyone but the name", RuleSpec{Match: MatchSpec{Not: &named}, Action: Deny}, "match.not.crawler.verified"},
		{"challenge everyone but the name", RuleSpec{Match: MatchSpec{Path: prefix("/"), Not: &named}, Action: Challenge}, "match.not.crawler.verified"},
		{"raise the score of everyone but the name", RuleSpec{Match: MatchSpec{Not: &named}, Action: Weigh, Weight: 5}, "match.not.crawler.verified"},
		{"deny everyone but impostors", RuleSpec{Match: MatchSpec{Not: &impostors}, Action: Deny}, "match.not.crawler.verified"},
		{"double negation", RuleSpec{Match: MatchSpec{Not: &MatchSpec{Not: &named}}, Action: Allow}, "match.not.not.crawler.verified"},
	}
	for _, tt := range refused {
		t.Run(tt.name, func(t *testing.T) {
			tt.rule.Name = "r"
			_, problems := Compile(Spec{DefaultAction: Challenge, Crawlers: testCatalog, Rules: []RuleSpec{tt.rule}})
			if len(problems) != 1 || problems[0].Field != tt.at || !strings.Contains(problems[0].Hint, "verified: true") {
				t.Errorf("problems = %+v", problems)
			}
		})
	}

	accepted := []struct {
		name string
		rule RuleSpec
	}{
		{"allow verified", RuleSpec{Match: verified, Action: Allow}},
		{"lower the score when verified", RuleSpec{Match: verified, Action: Weigh, Weight: -5}},
		{"deny by name", RuleSpec{Match: named, Action: Deny}},
		{"challenge by name", RuleSpec{Match: named, Action: Challenge}},
		{"raise the score by name", RuleSpec{Match: named, Action: Weigh, Weight: 5}},
		{"deny everyone but the genuine crawler", RuleSpec{Match: MatchSpec{Not: &verified}, Action: Deny}},
		{"deny by name, doubly negated", RuleSpec{Match: MatchSpec{Not: &MatchSpec{Not: &named}}, Action: Deny}},
		// "allow whoever does not claim the name" favours nobody for the name.
		{"allow everyone else", RuleSpec{Match: MatchSpec{Path: prefix("/x"), Not: &named}, Action: Allow}},
	}
	for _, tt := range accepted {
		t.Run(tt.name, func(t *testing.T) {
			tt.rule.Name = "r"
			if _, problems := Compile(Spec{DefaultAction: Challenge, Crawlers: testCatalog, Rules: []RuleSpec{tt.rule}}); len(problems) > 0 {
				t.Errorf("problems = %+v", problems)
			}
		})
	}
}

func TestCrawlerConditionProblems(t *testing.T) {
	tests := []struct {
		name    string
		spec    CrawlerSpec
		catalog *Catalog
		at, msg string
	}{
		{"empty", CrawlerSpec{}, testCatalog, "match.crawler", "empty"},
		{"unknown class", CrawlerSpec{Class: []string{"evil"}}, testCatalog, "match.crawler.class[0]", "not a known crawler class"},
		{"unknown name", CrawlerSpec{Name: []string{"GPTBot", "NoSuchBot"}}, testCatalog, "match.crawler.name[1]", "not a known crawler name"},
		{"empty class list", CrawlerSpec{Class: []string{}}, testCatalog, "match.crawler.class", "0 entries"},
		{"no definitions", CrawlerSpec{Class: []string{"training"}}, nil, "match.crawler", "no crawler definitions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := tt.spec
			_, problems := Compile(Spec{DefaultAction: Allow, Crawlers: tt.catalog,
				Rules: []RuleSpec{{Name: "r", Match: MatchSpec{Crawler: &spec}, Action: Deny}}})
			if len(problems) != 1 || problems[0].Field != tt.at || !strings.Contains(problems[0].Message, tt.msg) || problems[0].Hint == "" {
				t.Errorf("problems = %+v", problems)
			}
		})
	}
}

func TestCrawlerConditionDoesNotAllocate(t *testing.T) {
	e := mustCompile(t, Spec{DefaultAction: Allow, Crawlers: testCatalog, Rules: []RuleSpec{
		{Name: "a", Match: MatchSpec{Crawler: &CrawlerSpec{Class: []string{"search-engine"}, Verified: yes()}}, Action: Allow},
		{Name: "b", Match: MatchSpec{Crawler: &CrawlerSpec{Name: []string{"GPTBot"}}}, Action: Deny},
	}})
	r := crawlerRequest("GPTBot", "training", CrawlerImpostor)
	if n := testing.AllocsPerRun(100, func() { e.Evaluate(r) }); n != 0 {
		t.Errorf("Evaluate allocates %v times", n)
	}
}

func TestTrappedCondition(t *testing.T) {
	caught := request("GET", "h", "/", "x", "192.0.2.1")
	caught.Trapped = true
	free := request("GET", "h", "/", "x", "192.0.2.1")

	e := mustCompile(t, Spec{DefaultAction: Allow, Trap: true, Rules: []RuleSpec{
		{Name: "r", Match: MatchSpec{Trapped: yes()}, Action: Deny},
	}})
	if !e.UsesTrap() || e.Evaluate(caught).Action != Deny || e.Evaluate(free).Action != Allow {
		t.Error("trapped: true")
	}
	e = mustCompile(t, Spec{DefaultAction: Allow, Trap: true, Rules: []RuleSpec{
		{Name: "r", Match: MatchSpec{Trapped: no()}, Action: Deny},
	}})
	if e.Evaluate(caught).Action != Allow || e.Evaluate(free).Action != Deny {
		t.Error("trapped: false")
	}

	// With the trap off the condition could never hold; that is a mistake.
	_, problems := Compile(Spec{DefaultAction: Allow, Rules: []RuleSpec{
		{Name: "r", Match: MatchSpec{Trapped: yes()}, Action: Deny},
	}})
	if len(problems) != 1 || problems[0].Field != "match.trapped" || !strings.Contains(problems[0].Hint, "trap.enabled") {
		t.Errorf("problems = %+v", problems)
	}
}

func TestCountryCondition(t *testing.T) {
	from := func(code string) *Request {
		r := request("GET", "h", "/", "x", "192.0.2.1")
		if code != "" {
			r.Country = [2]byte{code[0], code[1]}
		}
		return r
	}
	e := mustCompile(t, Spec{DefaultAction: Allow, Countries: true, Rules: []RuleSpec{
		{Name: "r", Match: MatchSpec{Country: []string{"de", " AT "}}, Action: Deny},
	}})
	if !e.UsesCountries() {
		t.Error("UsesCountries() = false")
	}
	for code, want := range map[string]Action{"DE": Deny, "AT": Deny, "CH": Allow, "": Allow} {
		if got := e.Evaluate(from(code)).Action; got != want {
			t.Errorf("country %q: %s, want %s", code, got, want)
		}
	}

	// "Everyone except": an address whose country is not known is outside too.
	e = mustCompile(t, Spec{DefaultAction: Allow, Countries: true, Rules: []RuleSpec{
		{Name: "r", Match: MatchSpec{Not: &MatchSpec{Country: []string{"DE"}}}, Action: Challenge},
	}})
	for code, want := range map[string]Action{"DE": Allow, "FR": Challenge, "": Challenge} {
		if got := e.Evaluate(from(code)).Action; got != want {
			t.Errorf("not DE, country %q: %s, want %s", code, got, want)
		}
	}
	german := from("DE")
	if n := testing.AllocsPerRun(100, func() { e.Evaluate(german) }); n != 0 {
		t.Errorf("Evaluate allocates %v times", n)
	}
}

func TestCountryConditionProblems(t *testing.T) {
	tests := []struct {
		name      string
		list      []string
		countries bool
		at, msg   string
	}{
		{"no database", []string{"DE"}, false, "match.country", "no country database"},
		{"empty list", []string{}, true, "match.country", "0 entries"},
		{"a name instead of a code", []string{"Germany"}, true, "match.country[0]", "not a country code"},
		{"three letters", []string{"DE", "DEU"}, true, "match.country[1]", "not a country code"},
		{"digits", []string{"D1"}, true, "match.country[0]", "not a country code"},
		{"UK", []string{"uk"}, true, "match.country[0]", "not the code of the United Kingdom"},
		{"EU", []string{"EU"}, true, "match.country[0]", "not a country"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, problems := Compile(Spec{DefaultAction: Allow, Countries: tt.countries,
				Rules: []RuleSpec{{Name: "r", Match: MatchSpec{Country: tt.list}, Action: Deny}}})
			if len(problems) != 1 || problems[0].Field != tt.at || !strings.Contains(problems[0].Message, tt.msg) || problems[0].Hint == "" {
				t.Errorf("problems = %+v", problems)
			}
		})
	}
}

// Without data, a rule about countries decides nothing: neither "in" nor
// "not in" may be taken as true.
func TestRulesWithCountryAreSkippedWithoutData(t *testing.T) {
	e := mustCompile(t, Spec{DefaultAction: Allow, Countries: true, Rules: []RuleSpec{
		{Name: "outside", Match: MatchSpec{Not: &MatchSpec{Country: []string{"DE"}}}, Action: Deny},
		{Name: "nested", Match: MatchSpec{Any: []MatchSpec{{Path: prefix("/x")}, {Country: []string{"DE"}}}}, Action: Deny},
		{Name: "plain", Match: MatchSpec{Path: prefix("/admin")}, Action: Deny},
	}})
	r := request("GET", "h", "/x", "ua", "192.0.2.1")
	r.NoCountryData = true
	if got := e.Evaluate(r); got.Action != Allow {
		t.Errorf("without country data: %s by %s", got.Action, e.Sources()[got.Source].ID)
	}
	// Rules without a country condition still apply.
	r = request("GET", "h", "/admin", "ua", "192.0.2.1")
	r.NoCountryData = true
	if e.Evaluate(r).Action != Deny {
		t.Error("a rule without a country condition was skipped too")
	}
	// With data, an address of unknown country is outside Germany.
	r = request("GET", "h", "/", "ua", "192.0.2.1")
	if e.Evaluate(r).Action != Deny {
		t.Error("with data, an unknown country is not outside")
	}
}

// A roundabout address must never be treated better than a plain one. That
// holds for rules that let through a path, and equally for rules that
// restrict everything except a path.
func TestRoundaboutAddressesAreNeverFavoured(t *testing.T) {
	public := MatchSpec{Path: prefix("/public/")}
	notPublic := MatchSpec{Not: &public}
	roundabout := []string{
		"/admin/..;x/public/y", "/admin//../public/y", `/admin\..\public/y`, "/public;x/y",
		"/public/%2e%2e/admin", "/x/../public/y", "//public/y", "/public/a\x00b", "/public/\xff",
	}
	req := func(sent string) *Request {
		r := request("GET", "h", sent, "ua", "192.0.2.1")
		r.PathAltered = PathAltered(sent, "")
		return r
	}
	sets := []struct {
		name        string
		spec        Spec
		plain       Action // for /public/y
		elsewhere   Action // for /admin
		roundabouts Action
	}{
		{"allow the path, deny the rest",
			Spec{DefaultAction: Deny, Rules: []RuleSpec{{Name: "r", Match: public, Action: Allow}}}, Allow, Deny, Deny},
		{"deny everything except the path",
			Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "r", Match: notPublic, Action: Deny}}}, Allow, Deny, Deny},
		{"challenge everything except the path",
			Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "r", Match: notPublic, Action: Challenge}}}, Allow, Challenge, Challenge},
		{"score everything except the path",
			Spec{DefaultAction: Allow, Thresholds: []ThresholdSpec{{Weight: 5, Action: Deny}},
				Rules: []RuleSpec{{Name: "r", Match: notPublic, Action: Weigh, Weight: 5}}}, Allow, Deny, Deny},
		{"lower the score for the path",
			Spec{DefaultAction: Allow, Thresholds: []ThresholdSpec{{Weight: 5, Action: Deny}}, Rules: []RuleSpec{
				{Name: "all", Match: MatchSpec{UserAgent: contains("ua")}, Action: Weigh, Weight: 5},
				{Name: "r", Match: public, Action: Weigh, Weight: -5}}}, Allow, Deny, Deny},
		{"except the path, nested",
			Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "r", Action: Deny,
				Match: MatchSpec{All: []MatchSpec{{Not: &MatchSpec{Any: []MatchSpec{public, {Path: prefix("/static/")}}}}}}}}}, Allow, Deny, Deny},
		// A rule that restricts a path errs on the wide side: every spelling is caught.
		{"deny the path",
			Spec{DefaultAction: Allow, Rules: []RuleSpec{{Name: "r", Match: public, Action: Deny}}}, Deny, Allow, Deny},
		// "Allow everything except the path" favours whoever is NOT on it.
		{"allow everything except the path",
			Spec{DefaultAction: Deny, Rules: []RuleSpec{{Name: "r", Match: notPublic, Action: Allow}}}, Deny, Allow, Deny},
	}
	for _, set := range sets {
		t.Run(set.name, func(t *testing.T) {
			e := mustCompile(t, set.spec)
			if got := e.Evaluate(req("/public/y")).Action; got != set.plain {
				t.Errorf("/public/y: %s, want %s", got, set.plain)
			}
			if got := e.Evaluate(req("/admin")).Action; got != set.elsewhere {
				t.Errorf("/admin: %s, want %s", got, set.elsewhere)
			}
			for _, sent := range roundabout {
				r := req(sent)
				r.Path = NormalizePath(sent)
				if got := e.Evaluate(r).Action; got != set.roundabouts {
					t.Errorf("%q (normalised %q): %s, want %s", sent, r.Path, got, set.roundabouts)
				}
			}
		})
	}
}

func TestPathAltered(t *testing.T) {
	plain := []string{"/", "/a/b", "/a/b/", "/.well-known/x", "/Straße/ö", "/a.b/c-d_e~f", "/a b"}
	for _, p := range plain {
		if PathAltered(p, "") {
			t.Errorf("%q counts as roundabout", p)
		}
	}
	altered := []string{"//a", "/a//b", "/a/./b", "/a/../b", `/a\b`, "/a;x/b", "/a/%2e%2e/b", "/a%", "/a\x00", "/a\nb", "/a\x7f", "/a\xff", "/a\xc0\xae"}
	for _, p := range altered {
		if !PathAltered(p, "") {
			t.Errorf("%q does not count as roundabout", p)
		}
	}
	if !PathAltered("/a/b", "/a%2Fb") {
		t.Error("an address with a specially encoded form does not count as roundabout")
	}
}

func TestQueryCondition(t *testing.T) {
	with := func(q string) *Request {
		r := request("GET", "h", "/robots.txt", "ua", "192.0.2.1")
		r.Query = q
		return r
	}
	none := mustCompile(t, Spec{DefaultAction: Deny, Rules: []RuleSpec{
		{Name: "r", Match: MatchSpec{Path: &StringSpec{Equals: "/robots.txt"}, Query: &StringSpec{Present: no()}}, Action: Allow}}})
	if none.Evaluate(with("")).Action != Allow || none.Evaluate(with("q=node/5")).Action != Deny {
		t.Error("query: {present: false}")
	}
	has := mustCompile(t, Spec{DefaultAction: Allow, Rules: []RuleSpec{
		{Name: "r", Match: MatchSpec{Query: &StringSpec{Contains: "export=all"}}, Action: Deny}}})
	if has.Evaluate(with("a=1&EXPORT=all")).Action != Deny || has.Evaluate(with("a=1")).Action != Allow {
		t.Error("query: {contains}")
	}
}

// Exempting from the limits by something anyone can send would be a way
// round the limits.
func TestExemptionNeedsSomethingTheClientCannotChoose(t *testing.T) {
	tests := []struct {
		name  string
		match MatchSpec
		ok    bool
	}{
		{"by address", MatchSpec{IP: []string{"192.0.2.0/24"}}, true},
		{"by verified crawler", MatchSpec{Crawler: &CrawlerSpec{Class: []string{"search-engine"}, Verified: yes()}}, true},
		{"by address and path", MatchSpec{IP: []string{"192.0.2.1"}, Path: prefix("/api")}, true},
		{"by path", MatchSpec{Path: prefix("/api")}, false},
		{"by user agent", MatchSpec{UserAgent: contains("Monitor")}, false},
		{"by a header", MatchSpec{Header: map[string]*StringSpec{"X-Key": {Equals: "secret"}}}, false},
		{"by not being at an address", MatchSpec{Not: &MatchSpec{IP: []string{"192.0.2.1"}}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, problems := Compile(Spec{DefaultAction: Challenge, Crawlers: testCatalog,
				Rules: []RuleSpec{{Name: "r", Match: tt.match, Action: Allow, ExemptFromLimits: true}}})
			if tt.ok != (len(problems) == 0) {
				t.Errorf("problems = %+v", problems)
			}
			if !tt.ok && (len(problems) != 1 || problems[0].Field != "exempt_from_limits") {
				t.Errorf("problems = %+v", problems)
			}
		})
	}
}
