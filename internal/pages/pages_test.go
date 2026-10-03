package pages

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func renderer(t *testing.T) *Renderer {
	t.Helper()
	r, err := New()
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
		if got := pickLanguage(header); got != want {
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
				if strings.Contains(page, banned) {
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
		if len(r.locales[lang]) != len(keys) {
			t.Errorf("language %q has %d texts, the program uses %d: remove unused ones or register new ones in keys", lang, len(r.locales[lang]), len(keys))
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
