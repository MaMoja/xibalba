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
