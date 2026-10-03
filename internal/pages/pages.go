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
// language means adding a file there and its code to the order in New.
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
	"strconv"
	"strings"
)

//go:embed assets/page.html assets/style.css assets/locales/*.json
var assets embed.FS

// languages lists the supported languages. The first is used when the visitor
// states no preference or prefers none of them.
var languages = []string{"de", "en"}

// keys are the texts every locale file must contain.
var keys = []string{
	"language_name",
	"unavailable_title", "unavailable_text",
	"blocked_title", "blocked_text",
	"reference_label",
}

// Renderer writes the pages. Build one with New; it is safe for concurrent use.
type Renderer struct {
	tmpl    *template.Template
	css     template.CSS
	csp     string
	locales map[string]map[string]string
}

// New loads the embedded template and texts. It fails only if the files built
// into the program are broken, which the package's tests rule out.
func New() (*Renderer, error) {
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

	r := &Renderer{tmpl: tmpl, css: template.CSS(style), locales: map[string]map[string]string{}}
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
	Lang, Name, Title, Text, ReferenceLabel string
}

type view struct {
	CSS       template.CSS
	Primary   version
	Others    []version
	Reference string
}

func (r *Renderer) write(w http.ResponseWriter, req *http.Request, status int, kind, reference string) {
	primary := pickLanguage(req.Header.Get("Accept-Language"))
	v := view{CSS: r.css, Reference: reference}
	for _, lang := range languages {
		texts := r.locales[lang]
		ver := version{
			Lang:           lang,
			Name:           texts["language_name"],
			Title:          texts[kind+"_title"],
			Text:           texts[kind+"_text"],
			ReferenceLabel: texts["reference_label"],
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
// Without a usable preference it returns the first supported language.
func pickLanguage(header string) string {
	best, bestQ := languages[0], -1.0
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
