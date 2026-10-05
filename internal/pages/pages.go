// Package pages renders the small pages that Xibalba itself shows to a
// website's visitors: "website unavailable", "request blocked" and the
// security check (challenge).
//
// These pages are seen by real people, on the websites of authorities and
// firms, usually when something has gone wrong. They are therefore plain,
// calm and usable by everyone:
//
//   - self-contained: no image, no font, no request to any other host,
//     enforced by a Content-Security-Policy that forbids all of it. Only the
//     challenge page runs a script, a single one built into Xibalba and
//     named in the policy by its hash;
//   - accessible: one heading, correct language marking, readable contrast in
//     light and dark mode, fully usable with a keyboard and without JavaScript;
//   - bilingual: the language is chosen from the visitor's Accept-Language
//     header and the other language is one click away on the same page;
//   - discreet: nothing about the internal set-up is shown.
//
// The texts live in assets/locales, one JSON file per language. Adding a
// language means adding a file there and its code to the language list.
//
// A site owner can adapt the pages without touching those files: Options
// carries the operator's name, a contact line, the default language, and a
// replacement for any text in any language.
package pages

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed assets/page.html assets/challenge.html assets/challenge.js assets/style.css assets/locales/*.json
var assets embed.FS

// languages lists the supported languages.
var languages = []string{"de", "en"}

// keys are the texts every locale file must contain. Each can be replaced
// through Options.Texts.
var keys = []string{
	"language_name",
	"operator",
	"unavailable_title", "unavailable_text",
	"blocked_title", "blocked_text",
	"limited_title", "limited_text",
	"reference_label", "contact_label",
	"imprint_label", "privacy_label",
	"challenge_title", "challenge_text", "challenge_cookie",
	"challenge_text_script", "challenge_text_wait", "challenge_text_refresh", "challenge_automated",
	"challenge_try_again", "challenge_no_style",
	"challenge_working", "challenge_done",
	"challenge_manual", "challenge_button", "challenge_needs_script",
	"challenge_too_early", "challenge_retry",
}

// fixedKeys are texts that belong to Xibalba itself and cannot be replaced.
var fixedKeys = []string{"attribution_text", "attribution_sponsor"}

// Where the attribution line points.
const (
	// RepoURL is the project's home.
	RepoURL = "https://github.com/MaMoja/xibalba"
	// SponsorURL is where the project can be supported.
	SponsorURL = "https://github.com/sponsors/MaMoja"
)

// operatorPlaceholder is replaced, in every text, by the operator's name.
const operatorPlaceholder = "{operator}"

// Limits on what a site owner may put on the pages.
const (
	maxNameLength = 200
	maxTextLength = 1000
)

// Languages returns the codes of the supported languages.
func Languages() []string { return append([]string(nil), languages...) }

// TextKeys returns the names of the texts that can be replaced.
func TextKeys() []string { return append([]string(nil), keys...) }

// Options adapts the pages to a site.
type Options struct {
	// Operator is who runs the website, as it should appear in a sentence,
	// for example "Stadt Musterhausen". It replaces the neutral phrase "The
	// operator of this website" in every language. Empty keeps that phrase.
	Operator string
	// Contact says how to reach the operator, for example an e-mail address
	// or a telephone number. It is shown on the block page. Empty shows nothing.
	Contact string
	// ImprintURL and PrivacyURL are the addresses of the operator's
	// imprint and privacy policy. Each is linked at the bottom of every
	// page if set. A full address ("https://…") or a path on the
	// protected website ("/impressum").
	ImprintURL, PrivacyURL string
	// DefaultLanguage is used when the visitor states no preference or
	// prefers no supported language. Empty means the first supported language.
	DefaultLanguage string
	// Texts replaces individual texts: language code, then text key, then
	// the new text. A text may contain {operator}.
	Texts map[string]map[string]string
	// StatusChallenge and StatusBlocked are the HTTP status of the security
	// check and of "request blocked". Zero means 403. See ChallengeStatuses
	// and BlockedStatuses for what else is allowed.
	StatusChallenge, StatusBlocked int
	// HideAttribution removes the "Protected by Xibalba" line from the
	// bottom of every page.
	HideAttribution bool
	// TrapLink, if set, returns the address to hide in a page for the client
	// of a request, or "" for none. It is put inside an inert element:
	// invisible and unreachable for people, found by programs that collect
	// every address in the page text (see internal/trap).
	TrapLink func(*http.Request) string
}

// ChallengeStatuses and BlockedStatuses return the HTTP status codes the
// two pages can be sent with. 403 is the honest one and the default. 200 is
// what some services that build link previews insist on. The others are for
// operators whose monitoring or web server in front treats them differently.
func ChallengeStatuses() []int { return []int{200, 403, 429, 503} }

// BlockedStatuses: see ChallengeStatuses.
func BlockedStatuses() []int { return []int{200, 403, 404, 410, 429, 451, 503} }

func hasStatus(list []int, v int) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func statusList(list []int) string {
	out := make([]string, len(list))
	for i, v := range list {
		out[i] = strconv.Itoa(v)
	}
	return strings.Join(out, ", ")
}

// Problem is one mistake in Options.
type Problem struct {
	// Field is the path of the offending setting, such as "texts.de.blocked_text".
	Field string
	// Message says what is wrong.
	Message string
	// Hint says how to fix it.
	Hint string
}

var placeholderRE = regexp.MustCompile(`\{[^{}]*\}`)

// Check reports every mistake in opts.
func Check(opts Options) []Problem {
	var problems []Problem
	add := func(field, message, hint string) {
		problems = append(problems, Problem{Field: field, Message: message, Hint: hint})
	}
	checkText := func(field, value string, limit int, placeholders bool) {
		switch {
		case len(value) > limit:
			add(field, fmt.Sprintf("the text is %d characters long; the limit is %d", len(value), limit), "shorten it")
		case strings.ContainsAny(value, "\x00"):
			add(field, "the text contains a NUL character", "remove it")
		}
		for _, ph := range placeholderRE.FindAllString(value, -1) {
			if !placeholders || ph != operatorPlaceholder {
				add(field, fmt.Sprintf("%s is not a placeholder Xibalba knows", ph),
					"the only placeholder is {operator}, and only in pages.texts; write other braces as plain words")
			}
		}
	}

	checkText("operator", opts.Operator, maxNameLength, false)
	checkText("contact", opts.Contact, maxNameLength, false)
	for field, link := range map[string]string{"imprint_url": opts.ImprintURL, "privacy_url": opts.PrivacyURL} {
		if link == "" {
			continue
		}
		u, err := url.Parse(link)
		plain := err == nil && len(link) <= 500 && !strings.ContainsFunc(link, func(r rune) bool { return r <= ' ' || r == 0x7f })
		full := plain && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil
		path := plain && u.Scheme == "" && u.Host == "" && strings.HasPrefix(link, "/") && !strings.HasPrefix(link, "//")
		if !full && !path {
			add(field, "this is not an address a link can lead to",
				`give a full address such as "https://www.example.org/impressum", or a path on your website such as "/impressum"`)
		}
	}
	if opts.StatusChallenge != 0 && !hasStatus(ChallengeStatuses(), opts.StatusChallenge) {
		add("status.challenge", fmt.Sprintf("%d is not a status the security check can be sent with", opts.StatusChallenge),
			"use one of: "+statusList(ChallengeStatuses()))
	}
	if opts.StatusBlocked != 0 && !hasStatus(BlockedStatuses(), opts.StatusBlocked) {
		add("status.blocked", fmt.Sprintf("%d is not a status the block page can be sent with", opts.StatusBlocked),
			"use one of: "+statusList(BlockedStatuses()))
	}
	if opts.DefaultLanguage != "" && !has(languages, opts.DefaultLanguage) {
		add("default_language", fmt.Sprintf("%q is not a supported language", opts.DefaultLanguage),
			"use one of: "+strings.Join(languages, ", "))
	}

	langs := make([]string, 0, len(opts.Texts))
	for lang := range opts.Texts {
		langs = append(langs, lang)
	}
	sort.Strings(langs)
	for _, lang := range langs {
		if !has(languages, lang) {
			add("texts."+lang, fmt.Sprintf("%q is not a supported language", lang), "use one of: "+strings.Join(languages, ", "))
			continue
		}
		names := make([]string, 0, len(opts.Texts[lang]))
		for key := range opts.Texts[lang] {
			names = append(names, key)
		}
		sort.Strings(names)
		for _, key := range names {
			field := "texts." + lang + "." + key
			value := opts.Texts[lang][key]
			switch {
			case has(fixedKeys, key):
				add(field, fmt.Sprintf("%q belongs to the Xibalba line at the bottom of the page and cannot be reworded", key),
					"to remove the line, set pages.attribution: false (sponsor license required)")
			case !has(keys, key):
				add(field, fmt.Sprintf("%q is not a text Xibalba shows", key), "use one of: "+strings.Join(keys, ", "))
			case strings.TrimSpace(value) == "":
				add(field, "the text is empty", "write the text, or remove the entry to keep the built-in one")
			default:
				checkText(field, value, maxTextLength, key != "operator")
			}
		}
	}
	return problems
}

func has(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

// Renderer writes the pages. Build one with New; it is safe for concurrent use.
type Renderer struct {
	tmpl     *template.Template
	chTmpl   *template.Template // the challenge page
	css      template.CSS
	script   template.JS
	csp      string
	chCSP    string // policy of the challenge page: also allows its script and form
	plainCSP string // policy of the challenge page without a script
	fallback string // language used without a usable preference
	contact  string
	imprint  string
	privacy  string
	hideAttr bool
	statusCh int
	statusBl int
	trap     func(*http.Request) string
	locales  map[string]map[string]string
}

// New loads the embedded template and texts and applies opts. It fails if
// opts has mistakes (see Check) or if the files built into the program are
// broken, which the package's tests rule out.
func New(opts Options) (*Renderer, error) {
	if problems := Check(opts); len(problems) > 0 {
		return nil, fmt.Errorf("pages: %s: %s", problems[0].Field, problems[0].Message)
	}
	page, err := assets.ReadFile("assets/page.html")
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New("page").Parse(string(page))
	if err != nil {
		return nil, fmt.Errorf("page template: %w", err)
	}
	chPage, err := assets.ReadFile("assets/challenge.html")
	if err != nil {
		return nil, err
	}
	chTmpl, err := template.New("challenge").Parse(string(chPage))
	if err != nil {
		return nil, fmt.Errorf("challenge template: %w", err)
	}
	js, err := assets.ReadFile("assets/challenge.js")
	if err != nil {
		return nil, err
	}
	script := strings.TrimSpace(string(js))
	css, err := assets.ReadFile("assets/style.css")
	if err != nil {
		return nil, err
	}
	style := strings.TrimSpace(string(css))

	r := &Renderer{
		tmpl:     tmpl,
		chTmpl:   chTmpl,
		css:      template.CSS(style),
		script:   template.JS(script),
		fallback: languages[0],
		contact:  strings.TrimSpace(opts.Contact),
		imprint:  opts.ImprintURL,
		privacy:  opts.PrivacyURL,
		hideAttr: opts.HideAttribution,
		statusCh: http.StatusForbidden,
		statusBl: http.StatusForbidden,
		trap:     opts.TrapLink,
		locales:  map[string]map[string]string{},
	}
	if opts.DefaultLanguage != "" {
		r.fallback = opts.DefaultLanguage
	}
	if opts.StatusChallenge != 0 {
		r.statusCh = opts.StatusChallenge
	}
	if opts.StatusBlocked != 0 {
		r.statusBl = opts.StatusBlocked
	}
	for _, lang := range languages {
		data, err := assets.ReadFile("assets/locales/" + lang + ".json")
		if err != nil {
			return nil, fmt.Errorf("language %q: %w", lang, err)
		}
		texts := map[string]string{}
		if err := json.Unmarshal(data, &texts); err != nil {
			return nil, fmt.Errorf("language %q: %w", lang, err)
		}
		for _, key := range append(append([]string{}, keys...), fixedKeys...) {
			if strings.TrimSpace(texts[key]) == "" {
				return nil, fmt.Errorf("language %q: text %q is missing", lang, key)
			}
		}
		if len(texts) != len(keys)+len(fixedKeys) {
			return nil, fmt.Errorf("language %q: has %d texts, the program uses %d", lang, len(texts), len(keys)+len(fixedKeys))
		}

		// The site owner's adjustments: a name for all languages, then
		// replacements for single texts in this language.
		if name := strings.TrimSpace(opts.Operator); name != "" {
			texts["operator"] = name
		}
		for key, value := range opts.Texts[lang] {
			texts[key] = value
		}
		for key, value := range texts {
			if key != "operator" {
				texts[key] = strings.ReplaceAll(value, operatorPlaceholder, texts["operator"])
			}
		}
		r.locales[lang] = texts
	}

	// The only thing the page may load or run is its own style block,
	// identified by its hash.
	sum := sha256.Sum256([]byte(style))
	styleSrc := "style-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	r.csp = "default-src 'none'; " + styleSrc + "; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

	// The challenge page may additionally run its one script and send its
	// form back to this website. Nothing else.
	sum = sha256.Sum256([]byte(script))
	// The challenge page without a script (methods wait and refresh).
	r.plainCSP = "default-src 'none'; " + styleSrc + "; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
	r.chCSP = "default-src 'none'; " + styleSrc + "; script-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) +
		"'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
	return r, nil
}

func (r *Renderer) trapFor(req *http.Request) string {
	if r.trap == nil {
		return ""
	}
	return r.trap(req)
}

// Unavailable tells the visitor that the website cannot be reached right now.
// status is the HTTP status to send, normally 502, 503 or 504.
func (r *Renderer) Unavailable(w http.ResponseWriter, req *http.Request, status int) {
	r.write(w, req, status, "unavailable", "")
}

// Blocked tells the visitor that the request was refused. reference is a short
// code the visitor can pass on to the site owner; it identifies the rule, not
// the visitor.
func (r *Renderer) Blocked(w http.ResponseWriter, req *http.Request, reference string) {
	r.write(w, req, r.statusBl, "blocked", reference)
}

// Limited tells the visitor that too many requests came from their
// connection. retryAfter is how long until it is worth trying again.
func (r *Renderer) Limited(w http.ResponseWriter, req *http.Request, retryAfter time.Duration) {
	if seconds := int(math.Ceil(retryAfter.Seconds())); seconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
	}
	r.write(w, req, http.StatusTooManyRequests, "limited", "")
}

// ChallengeView is one task to show on the challenge page.
type ChallengeView struct {
	// Action is where the form is sent.
	Action string
	// Token is the signed task; Return is where the visitor goes afterwards.
	Token, Return string
	// Nonce and Difficulty are the proof of work for the script.
	Nonce      string
	Difficulty int
	// AllowButton offers the button: the whole check for the methods
	// wait and refresh, the path without JavaScript otherwise.
	AllowButton bool
	// Method is the kind of check: "pow", "script", "wait" or "refresh".
	Method string
	// WaitSeconds is how long the visitor has to wait.
	WaitSeconds int
	// RefreshURL, for the method refresh, is where the page sends the
	// browser by itself after WaitSeconds.
	RefreshURL string
	// StyleURL is a style sheet the page has to load (the css check).
	StyleURL string
	// Headless asks the script to report signs of automation.
	Headless bool
	// Meta are link-preview tags of the page that was asked for. They go
	// into the head, for the services that build the preview of a shared
	// link; a person never sees them.
	Meta []MetaTag
	// Notice selects a note about the previous attempt: "", "too_early",
	// "retry" or "automated".
	Notice string
}

// MetaTag is one tag for ChallengeView.Meta: <meta property="Key"
// content="Value">, or with Name set <meta name="Key" content="Value">.
type MetaTag struct {
	Key, Value string
	Name       bool
}

// scripted reports whether the check is answered by the page's script.
func (v ChallengeView) scripted() bool { return v.Method != "wait" && v.Method != "refresh" }

type challengeVersion struct {
	Lang, Name, Title, Text, Cookie string
}

// attribution is the "Protected by Xibalba" line.
type attribution struct {
	Text, Sponsor, RepoURL, SponsorURL string
}

// legalLink is a link to the operator's imprint or privacy policy.
type legalLink struct{ URL, Label string }

// legalFor returns the links the operator configured, labelled in lang.
func (r *Renderer) legalFor(lang string) []legalLink {
	var links []legalLink
	if r.imprint != "" {
		links = append(links, legalLink{r.imprint, r.locales[lang]["imprint_label"]})
	}
	if r.privacy != "" {
		links = append(links, legalLink{r.privacy, r.locales[lang]["privacy_label"]})
	}
	return links
}

// attributionFor returns the line in the given language, or nil if it is hidden.
func (r *Renderer) attributionFor(lang string) *attribution {
	if r.hideAttr {
		return nil
	}
	texts := r.locales[lang]
	return &attribution{
		Text: texts["attribution_text"], Sponsor: texts["attribution_sponsor"],
		RepoURL: RepoURL, SponsorURL: SponsorURL,
	}
}

type challengePage struct {
	Trap        string
	CSS         template.CSS
	Attribution *attribution
	Legal       []legalLink
	Script      template.JS
	Primary     challengeVersion
	Others      []challengeVersion
	ChallengeView
	Notice                                     string
	Scripted, Stopped                          bool
	Working, Done, Manual, Button, NeedsScript string
	TryAgain, NoStyle                          string
}

// Challenge shows the security check. It is sent with status 403 unless the
// operator chose another, and always marked as not to be kept or indexed,
// so that neither caches nor search engines take it for the page that was
// asked for.
func (r *Renderer) Challenge(w http.ResponseWriter, req *http.Request, v ChallengeView) {
	primary := pickLanguage(req.Header.Get("Accept-Language"), r.fallback)
	texts := r.locales[primary]
	p := challengePage{
		Trap: r.trapFor(req),
		CSS:  r.css, Script: r.script, ChallengeView: v, Attribution: r.attributionFor(primary), Legal: r.legalFor(primary),
		Working: texts["challenge_working"], Done: texts["challenge_done"],
		Manual: texts["challenge_manual"], Button: texts["challenge_button"],
		NeedsScript: texts["challenge_needs_script"],
		TryAgain:    texts["challenge_try_again"], NoStyle: texts["challenge_no_style"],
	}
	switch v.Notice {
	case "too_early":
		p.Notice = texts["challenge_too_early"]
	case "retry":
		p.Notice = texts["challenge_retry"]
	case "automated":
		p.Notice = texts["challenge_automated"]
		// The page says why the check failed and stops there. Trying again
		// by itself would only fail again, once a second, for ever.
		p.Stopped = true
	}
	p.Scripted = v.scripted()
	if p.Stopped {
		p.Script = ""
	}
	// What the page may do, and no more: the script only where the check
	// is answered by it, a style sheet of our own only for the css check.
	csp := r.chCSP
	if !v.scripted() {
		p.Script, csp = "", r.plainCSP
	}
	if v.StyleURL != "" {
		csp = strings.Replace(csp, "style-src ", "style-src 'self' ", 1)
	}
	textKey := "challenge_text"
	if v.Method == "script" || v.Method == "wait" || v.Method == "refresh" {
		textKey += "_" + v.Method
	}
	for _, lang := range languages {
		t := r.locales[lang]
		ver := challengeVersion{
			Lang: lang, Name: t["language_name"],
			Title: t["challenge_title"], Text: t[textKey], Cookie: t["challenge_cookie"],
		}
		if lang == primary {
			p.Primary = ver
		} else {
			p.Others = append(p.Others, ver)
		}
	}

	var body bytes.Buffer
	if err := r.chTmpl.Execute(&body, p); err != nil {
		http.Error(w, http.StatusText(r.statusCh), r.statusCh)
		return
	}
	r.send(w, req, r.statusCh, primary, csp, &body)
}

type version struct {
	Lang, Name, Title, Text, ReferenceLabel, ContactLabel string
}

type view struct {
	Trap        string
	CSS         template.CSS
	Attribution *attribution
	Legal       []legalLink
	Primary     version
	Others      []version
	Reference   string
	Contact     string
}

func (r *Renderer) write(w http.ResponseWriter, req *http.Request, status int, kind, reference string) {
	primary := pickLanguage(req.Header.Get("Accept-Language"), r.fallback)
	v := view{Trap: r.trapFor(req), CSS: r.css, Reference: reference, Attribution: r.attributionFor(primary), Legal: r.legalFor(primary)}
	if kind == "blocked" || kind == "limited" {
		v.Contact = r.contact
	}
	for _, lang := range languages {
		texts := r.locales[lang]
		ver := version{
			Lang:           lang,
			Name:           texts["language_name"],
			Title:          texts[kind+"_title"],
			Text:           texts[kind+"_text"],
			ReferenceLabel: texts["reference_label"],
			ContactLabel:   texts["contact_label"],
		}
		if lang == primary {
			v.Primary = ver
		} else {
			v.Others = append(v.Others, ver)
		}
	}

	// Render first, so a template failure cannot leave a half-written page.
	var body bytes.Buffer
	if err := r.tmpl.Execute(&body, v); err != nil {
		http.Error(w, http.StatusText(status), status)
		return
	}

	r.send(w, req, status, primary, r.csp, &body)
}

// send writes a rendered page with the headers every visitor page carries.
func (r *Renderer) send(w http.ResponseWriter, req *http.Request, status int, lang, csp string, body *bytes.Buffer) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Language", lang)
	h.Set("Content-Length", strconv.Itoa(body.Len()))
	h.Set("Content-Security-Policy", csp)
	h.Set("Cache-Control", "no-store")
	h.Set("Vary", "Accept-Language")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Robots-Tag", "noindex")
	w.WriteHeader(status)
	if req.Method != http.MethodHead {
		_, _ = w.Write(body.Bytes())
	}
}

// pickLanguage chooses the supported language the visitor prefers most,
// according to an Accept-Language header such as "en-GB,en;q=0.9,de;q=0.5".
// Without a usable preference it returns fallback.
func pickLanguage(header, fallback string) string {
	best, bestQ := fallback, -1.0
	for i, part := range strings.Split(header, ",") {
		if i >= 16 { // bounded work on absurd headers
			break
		}
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		q := 1.0
		if value, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			parsed, err := strconv.ParseFloat(value, 64)
			if err != nil || parsed < 0 || parsed > 1 {
				continue
			}
			q = parsed
		}
		if q == 0 {
			continue
		}
		base, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(tag)), "-")
		for _, lang := range languages {
			if base == lang && q > bestQ {
				best, bestQ = lang, q
			}
		}
	}
	return best
}
