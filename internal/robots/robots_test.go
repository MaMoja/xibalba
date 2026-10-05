package robots

import (
	"bytes"
	"net/netip"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/MaMoja/xibalba/internal/rules"
)

const sample = `# example
User-agent: *
Disallow: /admin/
Disallow: /private
Allow: /private/press/
Allow: /private/logo.png
Disallow: /*.pdf$
Disallow: /search?*q=
Crawl-delay: 10
Disallow:

User-agent: BadBot
User-agent: WorseBot
Disallow: /

User-agent: PickyBot
Disallow: /shop/
Sitemap: https://www.example.org/sitemap.xml
`

// compile turns the written rule file into an engine, as the configuration does.
func compile(t *testing.T, text string) *rules.Engine {
	t.Helper()
	var doc struct {
		Rules []rules.RuleSpec `yaml:"rules"`
	}
	if err := yaml.UnmarshalWithOptions([]byte(text), &doc, yaml.Strict()); err != nil {
		t.Fatalf("the output is not a rule file: %v\n%s", err, text)
	}
	engine, problems := rules.Compile(rules.Spec{DefaultAction: rules.Allow, Rules: doc.Rules})
	if len(problems) != 0 {
		t.Fatalf("the rules do not compile: %+v\n%s", problems, text)
	}
	return engine
}

func decide(e *rules.Engine, path, query, agent string) rules.Action {
	req := rules.Request{Method: "GET", Path: path, Query: query, UserAgent: agent, Client: netip.MustParseAddr("203.0.113.5")}
	return e.Evaluate(&req).Action
}

func TestConvert(t *testing.T) {
	var out bytes.Buffer
	notes, err := Convert(strings.NewReader(sample), &out, Options{})
	if err != nil {
		t.Fatal(err)
	}
	e := compile(t, out.String())
	cases := []struct {
		path, agent string
		want        rules.Action
	}{
		{"/", "Mozilla/5.0", rules.Allow},
		{"/admin/users", "Mozilla/5.0", rules.Challenge},
		{"/ADMIN/users", "Mozilla/5.0", rules.Challenge}, // a rule that restricts errs on the wide side
		{"/administrator", "Mozilla/5.0", rules.Allow},
		{"/private", "Mozilla/5.0", rules.Challenge},
		{"/private/x", "Mozilla/5.0", rules.Challenge},
		{"/private/press/2026", "Mozilla/5.0", rules.Allow},
		{"/private/logo.png", "Mozilla/5.0", rules.Allow},
		{"/docs/a.pdf", "Mozilla/5.0", rules.Challenge},
		{"/docs/a.pdf.html", "Mozilla/5.0", rules.Allow},
		{"/", "Mozilla/5.0 (compatible; BadBot/1.0)", rules.Deny},
		{"/anything", "WorseBot", rules.Deny},
		{"/shop/item", "PickyBot/2", rules.Challenge},
		{"/blog", "PickyBot/2", rules.Allow},
		{"/shop/item", "Mozilla/5.0", rules.Allow},
	}
	for _, c := range cases {
		if got := decide(e, c.path, "", c.agent); got != c.want {
			t.Errorf("%s as %q: %s, want %s", c.path, c.agent, got, c.want)
		}
	}
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "Crawl-delay") || !strings.Contains(joined, "lies about its name") {
		t.Errorf("notes: %v", notes)
	}

	out.Reset()
	if _, err := Convert(strings.NewReader(sample), &out, Options{Action: "deny", AgentAction: "challenge", Prefix: "from-robots"}); err != nil {
		t.Fatal(err)
	}
	e = compile(t, out.String())
	if decide(e, "/admin/x", "", "Mozilla/5.0") != rules.Deny || decide(e, "/", "", "BadBot") != rules.Challenge || !strings.Contains(out.String(), "name: from-robots-1\n") {
		t.Errorf("options not used:\n%s", out.String())
	}
}

func TestConvertHostileAndOddFiles(t *testing.T) {
	var out bytes.Buffer
	hostile := "User-agent: *\nDisallow: /a'b\nDisallow: /x\"y\nDisallow: /tab\there\nDisallow: /it's: {a: b}\nDisallow: relative\nDisallow: /ok\nDisallow: /(group)[x]+.\\d\nDisallow: /q*{1,99999}$\n" +
		"User-agent: Evil\"Bot\nUser-agent: Fine'Bot\nDisallow: /\nUser-agent: " + strings.Repeat("A", 500) + "\nDisallow: /\n"
	notes, err := Convert(strings.NewReader(hostile), &out, Options{})
	if err != nil {
		t.Fatal(err)
	}
	e := compile(t, out.String()) // whatever the file held, the output is a valid rule file
	if decide(e, "/a'b", "", "x") != rules.Challenge || decide(e, "/ok/1", "", "x") != rules.Challenge || decide(e, "/", "", "Fine'Bot") != rules.Deny {
		t.Errorf("rules:\n%s", out.String())
	}
	if decide(e, "/q1{1,99999}", "", "x") != rules.Challenge || decide(e, "/qqqq", "", "x") != rules.Allow {
		t.Error("characters of a pattern were not taken literally")
	}
	if !strings.Contains(strings.Join(notes, "\n"), "left out") {
		t.Errorf("notes: %v", notes)
	}

	for name, input := range map[string]string{
		"nothing disallowed": "User-agent: *\nDisallow:\nAllow: /\n",
		"empty":              "",
		"no group":           "Disallow: /x\n",
		"too large":          "User-agent: *\n" + strings.Repeat("Disallow: /"+strings.Repeat("a", 100)+"\n", 6000),
		"a huge line":        "User-agent: *\nDisallow: /" + strings.Repeat("a", 5000) + "\n",
	} {
		out.Reset()
		if _, err := Convert(strings.NewReader(input), &out, Options{}); err == nil || out.Len() != 0 {
			t.Errorf("%s: no error, or output written", name)
		}
	}
	for _, opts := range []Options{{Action: "allow"}, {AgentAction: "weigh"}, {Prefix: "Bad Name"}, {Prefix: "a: b"}} {
		if _, err := Convert(strings.NewReader(sample), &out, opts); err == nil {
			t.Errorf("%+v accepted", opts)
		}
	}

	// The whole site for everyone: written, with a warning.
	out.Reset()
	notes, err = Convert(strings.NewReader("User-agent: *\nDisallow: /\n"), &out, Options{})
	if err != nil || !strings.Contains(strings.Join(notes, "\n"), "whole site") {
		t.Errorf("err %v, notes %v", err, notes)
	}
	// Very many rules: bounded, and said.
	var many strings.Builder
	many.WriteString("User-agent: *\n")
	for i := 0; i < 2000; i++ {
		many.WriteString("Disallow: /p" + strings.Repeat("x", i%7) + "/" + string(rune('a'+i%26)) + "\n")
	}
	out.Reset()
	notes, err = Convert(strings.NewReader(many.String()), &out, Options{})
	if err != nil || strings.Count(out.String(), "- name:") != MaxRules || !strings.Contains(strings.Join(notes, "\n"), "left out") {
		t.Errorf("err %v, %d rules, notes %v", err, strings.Count(out.String(), "- name:"), notes)
	}
}
