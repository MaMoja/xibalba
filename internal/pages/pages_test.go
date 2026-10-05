package pages

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func renderer(t *testing.T) *Renderer {
	t.Helper()
	r, err := New(Options{})
	if err != nil {
		t.Fatalf("the embedded page assets are broken: %v", err)
	}
	return r
}

func get(acceptLanguage string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/some/page", nil)
	if acceptLanguage != "" {
		req.Header.Set("Accept-Language", acceptLanguage)
	}
	return req
}

func TestPickLanguage(t *testing.T) {
	tests := map[string]string{
		"":                                "de",
		"de":                              "de",
		"de-AT,de;q=0.9":                  "de",
		"en":                              "en",
		"en-GB,en;q=0.9,de;q=0.5":         "en",
		"fr-FR,fr;q=0.9,en;q=0.4":         "en",
		"fr,es":                           "de",
		"de;q=0.2,en;q=0.8":               "en",
		"EN-us":                           "en",
		"en;q=0":                          "de",
		"en;q=abc":                        "de",
		"en;q=7":                          "de",
		"*":                               "de",
		",,,;;;q=":                        "de",
		strings.Repeat("xx,", 500) + "en": "de", // the preference lies beyond the bound
	}
	for header, want := range tests {
		if got := pickLanguage(header, "de"); got != want {
			t.Errorf("pickLanguage(%.40q) = %q, want %q", header, got, want)
		}
	}
}

func TestUnavailable(t *testing.T) {
	r := renderer(t)
	for lang, title := range map[string]string{"de": "Die Website ist gerade nicht erreichbar", "en": "The website is currently unavailable"} {
		rec := httptest.NewRecorder()
		r.Unavailable(rec, get(lang), http.StatusBadGateway)
		page := rec.Body.String()

		if rec.Code != http.StatusBadGateway {
			t.Errorf("%s: status = %d", lang, rec.Code)
		}
		if !strings.Contains(page, `<html lang="`+lang+`">`) || !strings.Contains(page, "<h1>"+title+"</h1>") {
			t.Errorf("%s: wrong primary language:\n%s", lang, page)
		}
		if rec.Header().Get("Content-Language") != lang {
			t.Errorf("%s: Content-Language = %q", lang, rec.Header().Get("Content-Language"))
		}
		if strings.Contains(page, "<code>") {
			t.Errorf("%s: the unavailable page must not show a reference", lang)
		}
	}
}

func TestBlocked(t *testing.T) {
	r := renderer(t)
	rec := httptest.NewRecorder()
	r.Blocked(rec, get("en"), "a1b2c3d4")
	page := rec.Body.String()

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if !strings.Contains(page, "<h1>This request was blocked</h1>") {
		t.Errorf("wrong heading:\n%s", page)
	}
	if !strings.Contains(page, "The operator of this website does not allow requests of this kind.") {
		t.Errorf("the neutral operator phrase is missing:\n%s", page)
	}
	if strings.Contains(page, "{operator}") || strings.Contains(page, "Contact:") {
		t.Errorf("a placeholder or an empty contact line was sent:\n%s", page)
	}
	if n := strings.Count(page, "<code>a1b2c3d4</code>"); n != 2 {
		t.Errorf("the reference appears %d times, want once per language", n)
	}
}

// Every page, in every language, must meet the rules for visitor-facing pages.
func TestPagesAreSelfContainedAndAccessible(t *testing.T) {
	r := renderer(t)
	render := map[string]func(*httptest.ResponseRecorder, *http.Request){
		"unavailable": func(rec *httptest.ResponseRecorder, req *http.Request) { r.Unavailable(rec, req, 502) },
		"blocked":     func(rec *httptest.ResponseRecorder, req *http.Request) { r.Blocked(rec, req, "a1b2c3d4") },
		"limited":     func(rec *httptest.ResponseRecorder, req *http.Request) { r.Limited(rec, req, 90*time.Second) },
	}
	for name, fn := range render {
		for _, lang := range languages {
			rec := httptest.NewRecorder()
			fn(rec, get(lang))
			page := rec.Body.String()
			id := name + "/" + lang

			for _, want := range []string{"<!doctype html>", `<meta charset="utf-8">`, `name="viewport"`, "<title>", "<main>", "<details lang="} {
				if !strings.Contains(page, want) {
					t.Errorf("%s: missing %s", id, want)
				}
			}
			if n := strings.Count(page, "<h1"); n != 1 {
				t.Errorf("%s: %d h1 headings, want exactly one", id, n)
			}
			if regexp.MustCompile(`<title>\s*</title>`).MatchString(page) {
				t.Errorf("%s: empty title", id)
			}
			for _, banned := range []string{"<script", "<img", "<link", "<iframe", "http://", "https://", "src=", "@import", "url(", "onclick", "javascript:"} {
				if strings.Contains(withoutFooter(t, page), banned) {
					t.Errorf("%s: contains %q; the page must load nothing and run nothing", id, banned)
				}
			}
			// The other language must be present and marked as such.
			for _, other := range languages {
				if other != lang && !strings.Contains(page, `<details lang="`+other+`">`) {
					t.Errorf("%s: the %s version is not offered", id, other)
				}
			}

			h := rec.Header()
			csp := h.Get("Content-Security-Policy")
			if !strings.HasPrefix(csp, "default-src 'none'; style-src 'sha256-") || strings.Contains(csp, "unsafe") {
				t.Errorf("%s: weak Content-Security-Policy %q", id, csp)
			}
			want := map[string]string{
				"Content-Type":           "text/html; charset=utf-8",
				"Cache-Control":          "no-store",
				"Vary":                   "Accept-Language",
				"X-Content-Type-Options": "nosniff",
				"X-Robots-Tag":           "noindex",
			}
			for k, v := range want {
				if h.Get(k) != v {
					t.Errorf("%s: header %s = %q, want %q", id, k, h.Get(k), v)
				}
			}
		}
	}
}

// Whatever reaches the page as a reference is shown as text, never as markup.
func TestReferenceIsEscaped(t *testing.T) {
	rec := httptest.NewRecorder()
	renderer(t).Blocked(rec, get("de"), `"><script>alert(1)</script>`)
	if strings.Contains(rec.Body.String(), "<script>") {
		t.Errorf("the reference was not escaped:\n%s", rec.Body.String())
	}
}

func TestHeadRequestGetsNoBody(t *testing.T) {
	rec := httptest.NewRecorder()
	renderer(t).Unavailable(rec, httptest.NewRequest(http.MethodHead, "/", nil), http.StatusBadGateway)
	if rec.Body.Len() != 0 || rec.Header().Get("Content-Length") == "0" {
		t.Errorf("HEAD: body %d bytes, Content-Length %q", rec.Body.Len(), rec.Header().Get("Content-Length"))
	}
}

func TestEveryLanguageHasEveryText(t *testing.T) {
	r := renderer(t)
	for _, lang := range languages {
		for _, key := range keys {
			if strings.TrimSpace(r.locales[lang][key]) == "" {
				t.Errorf("language %q is missing text %q", lang, key)
			}
		}
		if want := len(keys) + len(fixedKeys); len(r.locales[lang]) != want {
			t.Errorf("language %q has %d texts, the program uses %d: remove unused ones or register new ones in keys", lang, len(r.locales[lang]), want)
		}
		if strings.Contains(r.locales[lang]["blocked_text"], "{") {
			t.Errorf("language %q: a placeholder was left in the block text", lang)
		}
	}
}

// The Content-Security-Policy allows exactly one style block, by hash. If the
// rendered block differed from the hashed one by a single byte, browsers would
// show the page unstyled.
func TestStyleBlockMatchesThePolicyHash(t *testing.T) {
	rec := httptest.NewRecorder()
	renderer(t).Blocked(rec, get("de"), "a1b2c3d4")
	page := rec.Body.String()

	start, end := strings.Index(page, "<style>"), strings.Index(page, "</style>")
	if start < 0 || end < 0 {
		t.Fatal("no style block")
	}
	sum := sha256.Sum256([]byte(page[start+len("<style>") : end]))
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, want) {
		t.Errorf("the policy %q does not allow the style block actually sent (%s)", csp, want)
	}
}

func blockedPage(t *testing.T, opts Options, acceptLanguage string) string {
	t.Helper()
	r, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec := httptest.NewRecorder()
	r.Blocked(rec, get(acceptLanguage), "a1b2c3d4")
	return rec.Body.String()
}

func TestOperatorNameReplacesTheNeutralPhrase(t *testing.T) {
	opts := Options{Operator: "Stadt Musterhausen"}
	german := blockedPage(t, opts, "de")
	english := blockedPage(t, opts, "en")
	if !strings.Contains(german, "<p>Stadt Musterhausen lässt Anfragen dieser Art nicht zu.") {
		t.Errorf("German page does not name the operator:\n%s", german)
	}
	if !strings.Contains(english, "<p>Stadt Musterhausen does not allow requests of this kind.") {
		t.Errorf("English page does not name the operator:\n%s", english)
	}
	if strings.Contains(german, "Der Betreiber dieser Website") {
		t.Errorf("the neutral phrase is still there:\n%s", german)
	}
}

func TestOperatorNamePerLanguage(t *testing.T) {
	opts := Options{
		Operator: "Musterhausen",
		Texts: map[string]map[string]string{
			"de": {"operator": "Die Stadt Musterhausen"},
			"en": {"operator": "The City of Musterhausen"},
		},
	}
	if page := blockedPage(t, opts, "de"); !strings.Contains(page, "<p>Die Stadt Musterhausen lässt") {
		t.Errorf("German operator text not used:\n%s", page)
	}
	if page := blockedPage(t, opts, "en"); !strings.Contains(page, "<p>The City of Musterhausen does not allow") {
		t.Errorf("English operator text not used:\n%s", page)
	}
}

func TestContactIsShownOnTheBlockPageOnly(t *testing.T) {
	opts := Options{Contact: "webmaster@musterhausen.example"}
	page := blockedPage(t, opts, "de")
	if n := strings.Count(page, "webmaster@musterhausen.example"); n != 2 {
		t.Errorf("contact appears %d times on the block page, want once per language:\n%s", n, page)
	}
	if !strings.Contains(page, "<p>Kontakt: webmaster@musterhausen.example</p>") || !strings.Contains(page, "<p>Contact: webmaster@musterhausen.example</p>") {
		t.Errorf("contact line is not labelled in both languages:\n%s", page)
	}

	r, _ := New(opts)
	rec := httptest.NewRecorder()
	r.Unavailable(rec, get("de"), 502)
	if strings.Contains(rec.Body.String(), "webmaster") {
		t.Error("the unavailable page shows the contact")
	}
}

func TestCustomTexts(t *testing.T) {
	opts := Options{
		Operator: "Musterfirma GmbH",
		Texts: map[string]map[string]string{
			"de": {
				"blocked_title": "Zugriff nicht möglich",
				"blocked_text":  "Automatisierte Abrufe sind bei {operator} nicht gestattet.",
			},
		},
	}
	german := blockedPage(t, opts, "de")
	if !strings.Contains(german, "<h1>Zugriff nicht möglich</h1>") || !strings.Contains(german, "<title>Zugriff nicht möglich</title>") {
		t.Errorf("custom title not used:\n%s", german)
	}
	if !strings.Contains(german, "<p>Automatisierte Abrufe sind bei Musterfirma GmbH nicht gestattet.</p>") {
		t.Errorf("custom text with the operator filled in not used:\n%s", german)
	}
	// English was not changed and keeps the built-in text with the name.
	if !strings.Contains(german, "<p>Musterfirma GmbH does not allow requests of this kind.") {
		t.Errorf("the English version lost its built-in text:\n%s", german)
	}
}

// Whatever a site owner writes is shown as text. It can never become markup,
// so a configuration mistake cannot break the page or inject a script.
func TestCustomTextIsEscaped(t *testing.T) {
	opts := Options{
		Operator: `<b>Firma</b> & Söhne`,
		Contact:  `<a href="https://example.org">hier</a>`,
		Texts:    map[string]map[string]string{"de": {"blocked_text": `{operator} <script>alert(1)</script>`}},
	}
	page := blockedPage(t, opts, "de")
	for _, banned := range []string{"<b>", "<script>", "<a href"} {
		if strings.Contains(withoutFooter(t, page), banned) {
			t.Errorf("custom text became markup (%s):\n%s", banned, page)
		}
	}
	if !strings.Contains(page, "&lt;b&gt;Firma&lt;/b&gt; &amp; Söhne") {
		t.Errorf("the operator name is not shown as written:\n%s", page)
	}
}

func TestDefaultLanguage(t *testing.T) {
	opts := Options{DefaultLanguage: "en"}
	if page := blockedPage(t, opts, ""); !strings.Contains(page, `<html lang="en">`) {
		t.Errorf("no preference: want the configured default language:\n%s", page)
	}
	if page := blockedPage(t, opts, "fr"); !strings.Contains(page, `<html lang="en">`) {
		t.Errorf("unsupported preference: want the configured default language:\n%s", page)
	}
	if page := blockedPage(t, opts, "de"); !strings.Contains(page, `<html lang="de">`) {
		t.Errorf("a visitor who prefers German must still get German:\n%s", page)
	}
}

func TestCheck(t *testing.T) {
	long := strings.Repeat("x", maxTextLength+1)
	tests := []struct {
		name      string
		opts      Options
		wantField string
		wantText  string
	}{
		{"unknown default language", Options{DefaultLanguage: "fr"}, "default_language", "not a supported language"},
		{"unknown language in texts", Options{Texts: map[string]map[string]string{"fr": {"blocked_title": "x"}}}, "texts.fr", "not a supported language"},
		{"unknown text key", Options{Texts: map[string]map[string]string{"de": {"blocked_heading": "x"}}}, "texts.de.blocked_heading", "not a text Xibalba shows"},
		{"empty text", Options{Texts: map[string]map[string]string{"de": {"blocked_title": "  "}}}, "texts.de.blocked_title", "empty"},
		{"text too long", Options{Texts: map[string]map[string]string{"en": {"blocked_text": long}}}, "texts.en.blocked_text", "limit"},
		{"operator too long", Options{Operator: long}, "operator", "limit"},
		{"contact too long", Options{Contact: long}, "contact", "limit"},
		{"unknown placeholder", Options{Texts: map[string]map[string]string{"de": {"blocked_text": "Hallo {name}"}}}, "texts.de.blocked_text", "{name} is not a placeholder"},
		{"placeholder in the operator name", Options{Operator: "{operator}"}, "operator", "not a placeholder"},
		{"operator text referring to itself", Options{Texts: map[string]map[string]string{"de": {"operator": "{operator} GmbH"}}}, "texts.de.operator", "not a placeholder"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := Check(tt.opts)
			if len(problems) != 1 || problems[0].Field != tt.wantField || !strings.Contains(problems[0].Message, tt.wantText) || problems[0].Hint == "" {
				t.Errorf("problems = %+v, want one at %q containing %q with a hint", problems, tt.wantField, tt.wantText)
			}
			if _, err := New(tt.opts); err == nil {
				t.Error("New accepted options that Check rejects")
			}
		})
	}
	if problems := Check(Options{}); len(problems) != 0 {
		t.Errorf("empty options have problems: %+v", problems)
	}
}

func challengePageFor(t *testing.T, opts Options, lang string, v ChallengeView) (*httptest.ResponseRecorder, string) {
	t.Helper()
	r, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec := httptest.NewRecorder()
	r.Challenge(rec, get(lang), v)
	return rec, rec.Body.String()
}

var task = ChallengeView{
	Action: "/.xibalba/verify", Token: "v1.payload.signature", Return: "/wiki/page?id=7",
	Nonce: "00112233445566778899aabbccddeeff", Difficulty: 18, AllowButton: true,
}

func TestChallengePage(t *testing.T) {
	rec, page := challengePageFor(t, Options{}, "de", task)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	for _, want := range []string{
		`<html lang="de">`,
		"<h1>Kurze Sicherheitsprüfung</h1>",
		"<title>Kurze Sicherheitsprüfung</title>",
		"Der Betreiber dieser Website schützt diese Seiten vor automatisierten Massenabrufen.",
		"ein Cookie gespeichert",
		`<form id="xibalba-form" method="post" action="/.xibalba/verify"`,
		`data-nonce="00112233445566778899aabbccddeeff"`,
		`data-difficulty="18"`,
		`data-working="Die Prüfung läuft …"`,
		`<input type="hidden" name="token" value="v1.payload.signature">`,
		`<input type="hidden" name="return" value="/wiki/page?id=7">`,
		`<input type="hidden" name="method" value="button">`,
		`<input type="hidden" name="solution" value="">`,
		`<p id="xibalba-status" role="status" aria-live="polite" hidden></p>`,
		`<button type="submit">Weiter</button>`,
		`<details lang="en">`,
		"<h2>A quick security check</h2>",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("challenge page is missing %s", want)
		}
	}
	if n := strings.Count(page, "<h1"); n != 1 {
		t.Errorf("%d h1 headings, want exactly one", n)
	}
	if strings.Contains(page, `role="alert"`) {
		t.Error("a first challenge shows a notice")
	}
	if strings.Contains(page, "{operator}") {
		t.Error("a placeholder was sent to the visitor")
	}
	for _, banned := range []string{"<img", "<link", "<iframe", "http://", "https://", "src=", "@import", "url(", "onclick", "javascript:", "eval(", "innerHTML", "XMLHttpRequest", "fetch("} {
		if strings.Contains(withoutFooter(t, page), banned) {
			t.Errorf("challenge page contains %q", banned)
		}
	}
	if n := strings.Count(page, "<script"); n != 1 {
		t.Errorf("%d script blocks, want exactly one", n)
	}
}

func TestChallengePageNotices(t *testing.T) {
	for notice, want := range map[string]string{
		"too_early": "That was a little too fast.",
		"retry":     "The check could not be completed and has been started again.",
	} {
		v := task
		v.Notice = notice
		_, page := challengePageFor(t, Options{}, "en", v)
		if !strings.Contains(page, `<p role="alert" class="notice">`+want) {
			t.Errorf("notice %q is not shown as an alert:\n%s", notice, page)
		}
	}
	v := task
	v.Notice = "something-unknown"
	if _, page := challengePageFor(t, Options{}, "en", v); strings.Contains(page, `role="alert"`) || strings.Contains(page, "something-unknown") {
		t.Error("an unknown notice code reached the page")
	}
}

func TestChallengePageWithoutButton(t *testing.T) {
	v := task
	v.AllowButton = false
	_, page := challengePageFor(t, Options{}, "en", v)
	if strings.Contains(page, "<button") {
		t.Error("the page offers a button although the path without JavaScript is off")
	}
	if !strings.Contains(page, "JavaScript must be switched on for this check.") {
		t.Errorf("the page does not say that JavaScript is needed:\n%s", page)
	}
}

// The task values come back from the client on a retry. They must never
// become markup or script.
func TestChallengeValuesAreEscaped(t *testing.T) {
	v := task
	v.Token = `"><script>alert(1)</script>`
	v.Return = `/x" onmouseover="alert(1)`
	v.Nonce = `"><img src=x>`
	_, page := challengePageFor(t, Options{}, "de", v)
	if strings.Count(page, "<script") != 1 || strings.Contains(page, "<img") || strings.Contains(page, `" onmouseover="`) {
		t.Errorf("a task value became markup:\n%s", page)
	}
}

func TestChallengePolicyAllowsExactlyItsScriptAndStyle(t *testing.T) {
	rec, page := challengePageFor(t, Options{}, "de", task)
	csp := rec.Header().Get("Content-Security-Policy")

	for tag, directive := range map[string]string{"style": "style-src", "script": "script-src"} {
		start, end := strings.Index(page, "<"+tag+">"), strings.Index(page, "</"+tag+">")
		if start < 0 || end < 0 {
			t.Fatalf("no %s block", tag)
		}
		sum := sha256.Sum256([]byte(page[start+len(tag)+2 : end]))
		want := directive + " 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
		if !strings.Contains(csp, want) {
			t.Errorf("the policy does not allow the %s block actually sent.\npolicy: %s\nneeded: %s", tag, csp, want)
		}
	}
	if !strings.HasPrefix(csp, "default-src 'none'; ") || strings.Contains(csp, "unsafe") || !strings.Contains(csp, "form-action 'self'") {
		t.Errorf("weak or wrong policy: %s", csp)
	}
}

func TestChallengePageUsesOperatorAndCustomTexts(t *testing.T) {
	opts := Options{
		Operator: "Stadt Musterhausen",
		Texts:    map[string]map[string]string{"de": {"challenge_button": "Fortfahren"}},
	}
	_, page := challengePageFor(t, opts, "de", task)
	if !strings.Contains(page, "Stadt Musterhausen schützt diese Seiten") || !strings.Contains(page, `<button type="submit">Fortfahren</button>`) {
		t.Errorf("operator name or custom button text not used:\n%s", page)
	}
}

var footerRE = regexp.MustCompile(`(?s)<footer>.*?</footer>\n?`)

// withoutFooter returns the page without Xibalba's attribution line, after
// checking that the line is exactly what it should be: two plain links and
// nothing that loads or runs.
func withoutFooter(t *testing.T, page string) string {
	t.Helper()
	footer := footerRE.FindString(page)
	if footer == "" {
		return page
	}
	if strings.Count(footer, "<a ") != 2 ||
		!strings.Contains(footer, `<a href="`+RepoURL+`" rel="noopener noreferrer">Xibalba</a>`) ||
		!strings.Contains(footer, `<a href="`+SponsorURL+`" rel="noopener noreferrer">`) {
		t.Errorf("the attribution line is not the two expected links:\n%s", footer)
	}
	for _, banned := range []string{"<script", "<img", "<link", "<iframe", "src=", "onclick", "javascript:", "target="} {
		if strings.Contains(footer, banned) {
			t.Errorf("the attribution line contains %q", banned)
		}
	}
	return footerRE.ReplaceAllString(page, "")
}

func TestAttributionIsShownByDefault(t *testing.T) {
	r := renderer(t)
	shows := map[string]func(*httptest.ResponseRecorder, *http.Request){
		"unavailable": func(rec *httptest.ResponseRecorder, req *http.Request) { r.Unavailable(rec, req, 502) },
		"blocked":     func(rec *httptest.ResponseRecorder, req *http.Request) { r.Blocked(rec, req, "a1b2c3d4") },
		"challenge":   func(rec *httptest.ResponseRecorder, req *http.Request) { r.Challenge(rec, req, task) },
	}
	wording := map[string][2]string{"de": {"Geschützt durch", "Projekt unterstützen"}, "en": {"Protected by", "Support the project"}}
	for name, show := range shows {
		for lang, words := range wording {
			rec := httptest.NewRecorder()
			show(rec, get(lang))
			page := rec.Body.String()
			footer := footerRE.FindString(page)
			if footer == "" {
				t.Errorf("%s/%s: no attribution line", name, lang)
				continue
			}
			if !strings.Contains(footer, words[0]+" <a ") || !strings.Contains(footer, ">"+words[1]+"</a>") {
				t.Errorf("%s/%s: attribution line is not in the page's language:\n%s", name, lang, footer)
			}
			if strings.Index(page, "<footer>") < strings.Index(page, "</main>") {
				t.Errorf("%s/%s: the attribution line must come after the page's content", name, lang)
			}
			withoutFooter(t, page)
		}
	}
}

func TestAttributionCanBeHidden(t *testing.T) {
	r, err := New(Options{HideAttribution: true})
	if err != nil {
		t.Fatal(err)
	}
	for name, show := range map[string]func(*httptest.ResponseRecorder, *http.Request){
		"unavailable": func(rec *httptest.ResponseRecorder, req *http.Request) { r.Unavailable(rec, req, 502) },
		"blocked":     func(rec *httptest.ResponseRecorder, req *http.Request) { r.Blocked(rec, req, "a1b2c3d4") },
		"challenge":   func(rec *httptest.ResponseRecorder, req *http.Request) { r.Challenge(rec, req, task) },
	} {
		rec := httptest.NewRecorder()
		show(rec, get("de"))
		page := rec.Body.String()
		for _, trace := range []string{"<footer", "github.com", "Geschützt durch", "Protected by", "Projekt unterstützen", ">Xibalba<"} {
			if strings.Contains(page, trace) {
				t.Errorf("%s: the page still shows %q although the attribution is hidden", name, trace)
			}
		}
	}
}

// The attribution line is Xibalba's own. Its wording cannot be replaced
// through the texts meant for the site owner.
func TestAttributionTextsCannotBeReplaced(t *testing.T) {
	for _, key := range fixedKeys {
		problems := Check(Options{Texts: map[string]map[string]string{"de": {key: "Etwas anderes"}}})
		if len(problems) != 1 || problems[0].Field != "texts.de."+key || !strings.Contains(problems[0].Message, "cannot be reworded") {
			t.Errorf("replacing %s: problems = %+v", key, problems)
		}
	}
	for _, key := range TextKeys() {
		if has(fixedKeys, key) {
			t.Errorf("%s is listed as replaceable", key)
		}
	}
}

func TestLimitedPage(t *testing.T) {
	r := renderer(t)
	rec := httptest.NewRecorder()
	r.Limited(rec, get("en"), 1500*time.Millisecond)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "2" {
		t.Errorf("status %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if !strings.Contains(rec.Body.String(), "<h1>Too many requests</h1>") {
		t.Errorf("body:\n%s", rec.Body)
	}
}

func TestTrapLinkIsInertAndOnlyThereWhenAsked(t *testing.T) {
	plain := renderer(t)
	rec := httptest.NewRecorder()
	plain.Blocked(rec, get("de"), "a1b2c3d4")
	if strings.Contains(rec.Body.String(), "<template") {
		t.Error("a page without a trap has a template element")
	}

	r, err := New(Options{TrapLink: func(req *http.Request) string {
		if req.Header.Get("X-No-Link") != "" {
			return ""
		}
		return "/.xibalba/trap/0123456789abcdef"
	}})
	if err != nil {
		t.Fatal(err)
	}
	pagesWith := map[string]func(*httptest.ResponseRecorder, *http.Request){
		"blocked":     func(rec *httptest.ResponseRecorder, req *http.Request) { r.Blocked(rec, req, "a1b2c3d4") },
		"limited":     func(rec *httptest.ResponseRecorder, req *http.Request) { r.Limited(rec, req, time.Second) },
		"unavailable": func(rec *httptest.ResponseRecorder, req *http.Request) { r.Unavailable(rec, req, 502) },
		"challenge": func(rec *httptest.ResponseRecorder, req *http.Request) {
			r.Challenge(rec, req, ChallengeView{Action: "/.xibalba/verify", Token: "t", Return: "/", Nonce: "n", Difficulty: 10, AllowButton: true})
		},
	}
	const want = `<template><a href="/.xibalba/trap/0123456789abcdef" rel="nofollow">-</a></template>`
	for name, fn := range pagesWith {
		rec := httptest.NewRecorder()
		fn(rec, get("de"))
		page := rec.Body.String()
		if strings.Count(page, want) != 1 {
			t.Errorf("%s: the trap link is not in the page exactly once inside a template element", name)
		}
		if strings.Count(page, "/.xibalba/trap/") != 1 {
			t.Errorf("%s: the trap address appears outside the template element", name)
		}
	}
	// No link for this client: no empty link in the page either.
	req := get("de")
	req.Header.Set("X-No-Link", "1")
	rec = httptest.NewRecorder()
	r.Blocked(rec, req, "a1b2c3d4")
	if strings.Contains(rec.Body.String(), "<template") {
		t.Error("a page without a link has a template element")
	}
}

// Links to imprint and privacy policy: on every page if configured, in the
// reader's language, and only to addresses a link may lead to.
func TestImprintAndPrivacyLinks(t *testing.T) {
	r, err := New(Options{ImprintURL: "https://www.example.org/impressum", PrivacyURL: "/datenschutz", HideAttribution: true})
	if err != nil {
		t.Fatal(err)
	}
	pages := map[string]func(http.ResponseWriter, *http.Request){
		"blocked":     func(w http.ResponseWriter, req *http.Request) { r.Blocked(w, req, "ref") },
		"unavailable": func(w http.ResponseWriter, req *http.Request) { r.Unavailable(w, req, 502) },
		"limited":     func(w http.ResponseWriter, req *http.Request) { r.Limited(w, req, time.Minute) },
		"challenge": func(w http.ResponseWriter, req *http.Request) {
			r.Challenge(w, req, ChallengeView{Action: "/x", Token: "t"})
		},
	}
	for name, show := range pages {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Language", "de")
		show(rec, req)
		body := rec.Body.String()
		for _, want := range []string{`<a href="https://www.example.org/impressum" rel="noreferrer">Impressum</a>`, `<a href="/datenschutz" rel="noreferrer">Datenschutzerklärung</a>`, "<footer>"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s page lacks %s", name, want)
			}
		}
	}
	// Without the settings, and with the attribution hidden, there is no footer at all.
	plain, _ := New(Options{HideAttribution: true})
	rec := httptest.NewRecorder()
	plain.Blocked(rec, httptest.NewRequest("GET", "/", nil), "ref")
	if strings.Contains(rec.Body.String(), "<footer>") {
		t.Error("an empty footer is shown")
	}

	for _, bad := range []string{"javascript:alert(1)", "//evil.example/x", "impressum", "https://", "https://user:pw@example.org/", "ftp://example.org/x",
		"https://example.org/a b", "/a\nb", "data:text/html,x", "https://example.org/" + strings.Repeat("a", 600), "\\\\evil"} {
		problems := Check(Options{ImprintURL: bad})
		if len(problems) != 1 || problems[0].Field != "imprint_url" {
			t.Errorf("imprint_url %q: %+v", bad, problems)
		}
	}
	for _, good := range []string{"https://www.example.org/impressum", "http://intern.example/i", "/impressum", "/de/datenschutz?x=1#oben"} {
		if problems := Check(Options{PrivacyURL: good}); len(problems) != 0 {
			t.Errorf("privacy_url %q: %+v", good, problems)
		}
	}
}

// Each kind of check gets the page it needs, and the page may do no more
// than that kind needs.
func TestChallengePageForEachMethod(t *testing.T) {
	r, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	show := func(v ChallengeView) (string, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Language", "en")
		v.Action, v.Token, v.Return, v.Nonce = "/.xibalba/verify", "tok", "/", "abc"
		r.Challenge(rec, req, v)
		return rec.Body.String(), rec.Header().Get("Content-Security-Policy")
	}

	body, csp := show(ChallengeView{Method: "pow", Difficulty: 18, AllowButton: true})
	if !strings.Contains(body, "<script>") || !strings.Contains(csp, "script-src 'sha256-") || strings.Contains(csp, "'self' 'sha256") ||
		!strings.Contains(body, "solving a short calculation") || !strings.Contains(body, `data-method="pow"`) {
		t.Errorf("pow page or policy is wrong: %s", csp)
	}

	body, _ = show(ChallengeView{Method: "script", WaitSeconds: 2})
	if !strings.Contains(body, "<script>") || !strings.Contains(body, "being checked briefly") || !strings.Contains(body, `data-wait="2"`) || strings.Contains(body, "<button") {
		t.Error("script page is wrong")
	}

	body, csp = show(ChallengeView{Method: "wait", WaitSeconds: 3, AllowButton: true})
	if strings.Contains(body, "<script") || strings.Contains(csp, "script-src") || !strings.Contains(csp, "form-action 'self'") ||
		!strings.Contains(body, "<button") || !strings.Contains(body, "then choose") || strings.Contains(body, "does not run JavaScript") || strings.Contains(body, "http-equiv") {
		t.Errorf("wait page or policy is wrong: %s", csp)
	}

	body, csp = show(ChallengeView{Method: "refresh", WaitSeconds: 4, AllowButton: true, RefreshURL: `/.xibalba/verify?token=a.b&return=%2F"><x>`})
	if strings.Contains(body, "<script") || strings.Contains(csp, "script-src") ||
		!strings.Contains(body, `<meta http-equiv="refresh" content="4;url=/.xibalba/verify?token=a.b&amp;return=%2F&#34;&gt;&lt;x&gt;">`) ||
		!strings.Contains(body, "<button") || !strings.Contains(body, "sent on automatically") {
		t.Errorf("refresh page or policy is wrong:\n%s", body[:600])
	}

	body, csp = show(ChallengeView{Method: "pow", Difficulty: 18, StyleURL: "/.xibalba/check.css?n=abc", Headless: true})
	for _, want := range []string{`<link rel="stylesheet" href="/.xibalba/check.css?n=abc">`, `data-css="1"`, `data-probe="1"`, `name="css"`, `name="probe"`, `data-nostyle="The check could not be finished`} {
		if !strings.Contains(body, want) {
			t.Errorf("page with extra checks lacks %s", want)
		}
	}
	if !strings.Contains(csp, "style-src 'self' 'sha256-") {
		t.Errorf("policy with the css check: %s", csp)
	}

	rec := httptest.NewRecorder()
	r.Challenge(rec, httptest.NewRequest("GET", "/", nil), ChallengeView{Method: "script", Notice: "automated", Return: "/"})
	if !strings.Contains(rec.Body.String(), `role="alert"`) || !strings.Contains(rec.Body.String(), "von einem Programm gesteuert") {
		t.Error("the notice about automation is missing")
	}
	if !strings.Contains(rec.Body.String(), `<a href="/">Erneut versuchen</a>`) {
		t.Error("the stopped page offers no way to try again")
	}
	if strings.Contains(rec.Body.String(), "<script") || strings.Contains(rec.Body.String(), "<form") {
		t.Error("after a report of automation the page tries again by itself")
	}
}

func TestPreviewTagsOnTheChallengePage(t *testing.T) {
	r := renderer(t)
	rec := httptest.NewRecorder()
	r.Challenge(rec, httptest.NewRequest("GET", "/wiki", nil), ChallengeView{Action: "/.xibalba/verify", Method: "pow", Meta: []MetaTag{
		{Key: "og:title", Value: `Town "hall" <script>alert(1)</script>`},
		{Key: "description", Value: "A & B", Name: true},
		{Key: "og:image", Value: "javascript:alert(1)"},
	}})
	body := rec.Body.String()
	for _, want := range []string{
		`<meta property="og:title" content="Town &#34;hall&#34; &lt;script&gt;alert(1)&lt;/script&gt;">`,
		`<meta name="description" content="A &amp; B">`,
		`<meta property="og:image" content="javascript:alert(1)">`, // plain text in an attribute nothing runs
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	if strings.Count(body, "<script>") != 1 || strings.Index(body, "og:title") > strings.Index(body, "</head>") {
		t.Errorf("tags broke out of the head:\n%s", body)
	}
	if got := rec.Header().Get("X-Robots-Tag"); got != "noindex" {
		t.Errorf("X-Robots-Tag = %q", got)
	}

	rec = httptest.NewRecorder()
	r.Challenge(rec, httptest.NewRequest("GET", "/wiki", nil), ChallengeView{Action: "/.xibalba/verify", Method: "pow"})
	if strings.Contains(rec.Body.String(), "og:") {
		t.Error("tags without any given")
	}
}

func TestStatusOfThePages(t *testing.T) {
	r := renderer(t)
	rec := httptest.NewRecorder()
	r.Challenge(rec, httptest.NewRequest("GET", "/", nil), ChallengeView{Method: "pow"})
	if rec.Code != 403 {
		t.Errorf("challenge: %d by default", rec.Code)
	}
	r, err := New(Options{StatusChallenge: 200, StatusBlocked: 404})
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	r.Challenge(rec, httptest.NewRequest("GET", "/", nil), ChallengeView{Method: "pow"})
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Robots-Tag") != "noindex" {
		t.Errorf("challenge: %d %v", rec.Code, rec.Header())
	}
	rec = httptest.NewRecorder()
	r.Blocked(rec, httptest.NewRequest("GET", "/", nil), "ref")
	if rec.Code != 404 {
		t.Errorf("blocked: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	r.Limited(rec, httptest.NewRequest("GET", "/", nil), time.Second)
	if rec.Code != 429 {
		t.Errorf("limited: %d", rec.Code)
	}
	for _, bad := range []Options{{StatusChallenge: 302}, {StatusChallenge: 404}, {StatusBlocked: 500}, {StatusBlocked: 301}, {StatusChallenge: -1}} {
		if len(Check(bad)) != 1 {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func TestMemoryScriptIsOnlyOnPagesThatNeedIt(t *testing.T) {
	r := renderer(t)
	page := func(v ChallengeView) (string, string) {
		rec := httptest.NewRecorder()
		r.Challenge(rec, httptest.NewRequest("GET", "/", nil), v)
		return rec.Body.String(), rec.Header().Get("Content-Security-Policy")
	}
	body, csp := page(ChallengeView{Action: "/.xibalba/verify", Method: "pow-memory", Nonce: "abc", Difficulty: 5, Memory: 4})
	if strings.Count(body, "<script>") != 2 || !strings.Contains(body, "xibalbaMemory = {") || !strings.Contains(body, `data-memory="4"`) {
		t.Errorf("the page for pow-memory lacks its second script or its size")
	}
	if strings.Index(body, "xibalbaMemory = {") > strings.Index(body, `getElementById("xibalba-form")`) {
		t.Error("the memory function comes after the script that calls it")
	}
	// Each script is named in the policy by its own hash, and nothing else is allowed.
	hashes := regexp.MustCompile(`'sha256-[A-Za-z0-9+/=]+'`).FindAllString(csp[strings.Index(csp, "script-src"):], -1)
	if len(hashes) < 2 || strings.Contains(csp, "unsafe") {
		t.Fatalf("policy: %s", csp)
	}
	for _, script := range regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindAllStringSubmatch(body, -1) {
		sum := sha256.Sum256([]byte(script[1]))
		if !strings.Contains(csp, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'") {
			t.Error("a script on the page is not the one the policy names")
		}
	}

	for _, v := range []ChallengeView{{Method: "pow", Nonce: "abc", Difficulty: 18}, {Method: "script", Nonce: "abc"}, {Method: "wait"}, {Method: "pow-memory", Notice: "automated"}} {
		body, csp := page(v)
		if strings.Contains(body, "xibalbaMemory = {") || strings.Contains(body, `data-memory="`) || csp == r.memCSP {
			t.Errorf("method %s, notice %q: the memory script is on the page", v.Method, v.Notice)
		}
	}
	if len(r.memory) > 12<<10 {
		t.Errorf("the memory script has grown to %d bytes", len(r.memory))
	}
}

func TestResendAndNoMemoryTextOnThePage(t *testing.T) {
	r := renderer(t)
	rec := httptest.NewRecorder()
	r.Challenge(rec, httptest.NewRequest("GET", "/", nil), ChallengeView{Method: "pow-memory", Nonce: "abc", Difficulty: 5, Memory: 4, Resend: `12"><script>`})
	body := rec.Body.String()
	if !strings.Contains(body, `data-resend="12&#34;&gt;&lt;script&gt;"`) || !strings.Contains(body, `data-nomemory="`) || strings.Count(body, "<script>") != 2 {
		t.Errorf("resend is missing or not escaped:\n%s", body[:1500])
	}
	rec = httptest.NewRecorder()
	r.Challenge(rec, httptest.NewRequest("GET", "/", nil), ChallengeView{Method: "pow", Nonce: "abc", Difficulty: 18})
	if strings.Contains(rec.Body.String(), `data-resend="`) || strings.Contains(rec.Body.String(), `data-nomemory="`) {
		t.Error("resend or the memory text on a page that needs neither")
	}
}

func TestHtmxIsToldToLoadTheWholePage(t *testing.T) {
	r := renderer(t)
	for header, want := range map[string]string{"true": "true", "": "", "false": "", "1": ""} {
		req := httptest.NewRequest("GET", "/part", nil)
		if header != "" {
			req.Header.Set("HX-Request", header)
		}
		rec := httptest.NewRecorder()
		r.Challenge(rec, req, ChallengeView{Method: "pow", Nonce: "abc", Difficulty: 18})
		if got := rec.Header().Get("HX-Refresh"); got != want {
			t.Errorf("HX-Request %q: HX-Refresh %q, want %q", header, got, want)
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/part", nil)
	req.Header.Set("HX-Request", "true")
	r.Blocked(rec, req, "ref")
	if rec.Header().Get("HX-Refresh") != "" {
		t.Error("the block page asks for a reload, which would only block again")
	}
}
