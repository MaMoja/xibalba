// Package pages renders the small pages that Xibalba itself shows to a
// website's visitors: "website unavailable" and "request blocked".
//
// These pages are seen by real people, on the websites of authorities and
// firms, usually when something has gone wrong. They are therefore plain,
// calm and usable by everyone:
//
//   - self-contained: no script, no image, no font, no request to any other
//     host, enforced by a Content-Security-Policy that forbids all of it;
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
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

//go:embed assets/page.html assets/style.css assets/locales/*.json
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
	"reference_label", "contact_label",
}

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
	// DefaultLanguage is used when the visitor states no preference or
	// prefers no supported language. Empty means the first supported language.
	DefaultLanguage string
	// Texts replaces individual texts: language code, then text key, then
	// the new text. A text may contain {operator}.
	Texts map[string]map[string]string
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
	css      template.CSS
	csp      string
	fallback string // language used without a usable preference
	contact  string
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
	css, err := assets.ReadFile("assets/style.css")
	if err != nil {
		return nil, err
	}
	style := strings.TrimSpace(string(css))

	r := &Renderer{
		tmpl:     tmpl,
		css:      template.CSS(style),
		fallback: languages[0],
		contact:  strings.TrimSpace(opts.Contact),
		locales:  map[string]map[string]string{},
	}
	if opts.DefaultLanguage != "" {
		r.fallback = opts.DefaultLanguage
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
		for _, key := range keys {
			if strings.TrimSpace(texts[key]) == "" {
				return nil, fmt.Errorf("language %q: text %q is missing", lang, key)
			}
		}
		if len(texts) != len(keys) {
			return nil, fmt.Errorf("language %q: has %d texts, the program uses %d", lang, len(texts), len(keys))
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
	r.csp = "default-src 'none'; style-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) +
		"'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	return r, nil
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
	r.write(w, req, http.StatusForbidden, "blocked", reference)
}

type version struct {
	Lang, Name, Title, Text, ReferenceLabel, ContactLabel string
}

type view struct {
	CSS       template.CSS
	Primary   version
	Others    []version
	Reference string
	Contact   string
}

func (r *Renderer) write(w http.ResponseWriter, req *http.Request, status int, kind, reference string) {
	primary := pickLanguage(req.Header.Get("Accept-Language"), r.fallback)
	v := view{CSS: r.css, Reference: reference}
	if kind == "blocked" {
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

	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Language", primary)
	h.Set("Content-Length", strconv.Itoa(body.Len()))
	h.Set("Content-Security-Policy", r.csp)
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
