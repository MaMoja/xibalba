package verdict

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/MaMoja/xibalba/internal/clientip"
)

// fake decides by the path that was asked for and notes what it was given.
type fake struct {
	served, paged []*http.Request
	limited       bool
	retry         time.Duration
}

func (f *fake) write(w http.ResponseWriter, r *http.Request, next http.Handler) string {
	switch {
	case strings.HasPrefix(r.URL.Path, "/wiki"):
		if r.Header.Get("Cookie") == "pass=1" {
			break
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Length", "14")
		w.WriteHeader(http.StatusOK) // an operator may have chosen 200 for the page
		_, _ = io.WriteString(w, "challenge page")
		return "challenge"
	case strings.HasPrefix(r.URL.Path, "/admin"), strings.Contains(r.URL.Path, "/../admin"), r.Method == "POST":
		w.WriteHeader(http.StatusNotFound) // and 404 for the block page
		_, _ = io.WriteString(w, "block page")
		return "deny"
	case strings.HasPrefix(r.URL.Path, "/busy"):
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, "limited page")
		return "limited"
	case strings.HasPrefix(r.URL.Path, "/broken"):
		w.WriteHeader(http.StatusServiceUnavailable)
		return "unavailable"
	}
	next.ServeHTTP(w, r)
	return "pass"
}

func (f *fake) Serve(w http.ResponseWriter, r *http.Request, next http.Handler) string {
	f.served = append(f.served, r)
	return f.write(w, r, next)
}

func (f *fake) Page(w http.ResponseWriter, r *http.Request, limited bool, retry time.Duration, next http.Handler) string {
	f.paged = append(f.paged, r)
	f.limited, f.retry = limited, retry
	return f.write(w, r, next)
}

func ask(h *Handler, path string, trusted bool, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	req.Host = "xibalba.internal:8080"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req = req.WithContext(clientip.NewContext(req.Context(), clientip.Info{
		Client: netip.MustParseAddr("203.0.113.5"), Peer: netip.MustParseAddr("127.0.0.1"), PeerTrusted: trusted,
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCheck(t *testing.T) {
	cases := []struct {
		name, uri, method string
		status            int
		verdict, body     string
	}{
		{"allowed", "/page?x=1", "GET", 204, "pass", ""},
		{"check due", "/wiki/Start", "GET", 401, "challenge", "challenge page"},
		{"denied", "/admin", "GET", 403, "deny", "block page"},
		{"denied by method", "/form", "POST", 403, "deny", "block page"},
		{"limited", "/busy", "GET", 403, "limited", "limited page"},
		{"failure inside", "/broken", "GET", 503, "unavailable", ""},
		{"dressed up as xibalba's own address", "/.xibalba/../admin", "GET", 403, "deny", "block page"},
		{"webdav method", "/page", "VERSION-CONTROL", 204, "pass", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fake{}
			rec := ask(New(f), CheckPath, true, map[string]string{
				"X-Forwarded-Uri": c.uri, "X-Forwarded-Method": c.method, "X-Forwarded-Host": "www.example.org", "User-Agent": "Browser",
			})
			if rec.Code != c.status || rec.Header().Get(HeaderVerdict) != c.verdict || rec.Body.String() != c.body {
				t.Fatalf("got %d %q %q", rec.Code, rec.Header().Get(HeaderVerdict), rec.Body)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Error("the answer may be stored")
			}
			r := f.served[0]
			if c.uri == "/.xibalba/../admin" {
				return
			}
			if r.URL.RequestURI() != c.uri || r.Method != c.method || r.Host != "www.example.org" || r.Header.Get("User-Agent") != "Browser" {
				t.Errorf("decided about %s %s on %s", r.Method, r.URL.RequestURI(), r.Host)
			}
			if r.Header.Get("X-Forwarded-Uri") != "" || r.Header.Get("X-Forwarded-Method") != "" {
				t.Error("the web server's own headers reached the rules")
			}
			if info, _ := clientip.FromContext(r.Context()); info.Client.String() != "203.0.113.5" {
				t.Errorf("client %s", info.Client)
			}
		})
	}
}

func TestCheckKeepsThePagesHeadersAndLength(t *testing.T) {
	rec := ask(New(&fake{}), CheckPath, true, map[string]string{"X-Forwarded-Uri": "/busy"})
	if rec.Header().Get("Retry-After") != "42" || rec.Header().Get("Content-Length") != "12" {
		t.Errorf("headers: %v", rec.Header())
	}
	rec = ask(New(&fake{}), CheckPath, true, map[string]string{"X-Forwarded-Uri": "/"})
	if rec.Header().Get("Content-Length") != "" || rec.Body.Len() != 0 {
		t.Errorf("a pass has a body: %v", rec.Header())
	}
}

func TestQuestionsThatAreNotAnswered(t *testing.T) {
	cases := []struct {
		name    string
		trusted bool
		headers map[string]string
	}{
		{"not from a trusted proxy", false, map[string]string{"X-Forwarded-Uri": "/admin"}},
		{"no address", true, nil},
		{"full address", true, map[string]string{"X-Forwarded-Uri": "https://other.example/x"}},
		{"address without a path", true, map[string]string{"X-Forwarded-Uri": "admin"}},
		{"star", true, map[string]string{"X-Forwarded-Uri": "*"}},
		{"broken escape", true, map[string]string{"X-Forwarded-Uri": "/a%zz"}},
		{"huge address", true, map[string]string{"X-Forwarded-Uri": "/" + strings.Repeat("a", 9000)}},
		{"method", true, map[string]string{"X-Forwarded-Uri": "/", "X-Forwarded-Method": "GET /x HTTP/1.1"}},
		{"two addresses", true, nil},
	}
	for _, path := range []string{CheckPath, PagePath} {
		for _, c := range cases {
			t.Run(path+" "+c.name, func(t *testing.T) {
				f := &fake{}
				h := New(f)
				rec := ask(h, path, c.trusted, c.headers)
				if rec.Code != 403 || len(f.served)+len(f.paged) != 0 || h.Counts().Refused != 1 {
					t.Errorf("got %d, decided %d times, refused %d", rec.Code, len(f.served)+len(f.paged), h.Counts().Refused)
				}
			})
		}
	}
	if rec := ask(New(&fake{}), "/.xibalba/other", true, nil); rec.Code != 404 {
		t.Errorf("another address: %d", rec.Code)
	}
}

func TestPage(t *testing.T) {
	f := &fake{}
	h := New(f)
	rec := ask(h, PagePath, true, map[string]string{"X-Forwarded-Uri": "/wiki/Start?a=1", "X-Forwarded-Method": "GET", HeaderVerdict: "challenge"})
	if rec.Code != 200 || rec.Body.String() != "challenge page" {
		t.Errorf("the page keeps its own status: %d %q", rec.Code, rec.Body)
	}
	rec = ask(h, PagePath, true, map[string]string{"X-Forwarded-Uri": "/admin", HeaderVerdict: "deny"})
	if rec.Code != 404 || rec.Body.String() != "block page" {
		t.Errorf("block page: %d %q", rec.Code, rec.Body)
	}
	ask(h, PagePath, true, map[string]string{"X-Forwarded-Uri": "/busy", HeaderVerdict: "limited", HeaderRetry: "42"})
	if !f.limited || f.retry != 42*time.Second {
		t.Errorf("what the limit said was not passed on: %v %s", f.limited, f.retry)
	}
	ask(h, PagePath, true, map[string]string{"X-Forwarded-Uri": "/busy", HeaderVerdict: "limited", HeaderRetry: "99999999999"})
	if f.retry > 24*time.Hour {
		t.Errorf("retry %s", f.retry)
	}
	if len(f.served) != 0 || h.Counts() != (Counts{}) {
		t.Errorf("a page was counted as a check: %+v", h.Counts())
	}
	// Passed meanwhile: back to where the visitor wanted to go.
	rec = ask(h, PagePath, true, map[string]string{"X-Forwarded-Uri": "/wiki/Start?a=1", "Cookie": "pass=1", HeaderVerdict: "challenge"})
	if rec.Code != 303 || rec.Header().Get("Location") != "/wiki/Start?a=1" {
		t.Errorf("passed meanwhile: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	for _, uri := range []string{"//other.example/x", "/.xibalba/page", "/\\other.example"} {
		rec = ask(h, PagePath, true, map[string]string{"X-Forwarded-Uri": uri, HeaderVerdict: "challenge"})
		// On this website, whatever the address was: never "//host" or "/\host".
		if loc := rec.Header().Get("Location"); rec.Code == 303 && loc != "/" && loc != "/%5Cother.example" {
			t.Errorf("%s leads to %q", uri, loc)
		}
	}
}

func TestCounts(t *testing.T) {
	h := New(&fake{})
	for _, uri := range []string{"/", "/", "/wiki", "/admin", "/busy", "/broken"} {
		ask(h, CheckPath, true, map[string]string{"X-Forwarded-Uri": uri})
	}
	ask(h, CheckPath, false, nil)
	want := Counts{Pass: 2, Challenge: 1, Deny: 1, Limited: 1, Unavailable: 1, Refused: 1}
	if got := h.Counts(); got != want {
		t.Errorf("got %+v", got)
	}
}

// The web server may ask for the page after a refusal of its own. That is
// not ours to explain, and the visitor must not be sent in a circle.
func TestPageWithoutARefusalOfOurs(t *testing.T) {
	for _, hint := range []string{"", "pass", "nonsense"} {
		f := &fake{}
		for _, cookie := range []string{"", "pass=1"} {
			rec := ask(New(f), PagePath, true, map[string]string{"X-Forwarded-Uri": "/wiki/Start", HeaderVerdict: hint, "Cookie": cookie})
			if rec.Code != 403 || rec.Header().Get("Location") != "" || strings.Contains(rec.Body.String(), "page") {
				t.Errorf("hint %q, cookie %q: %d %q to %q", hint, cookie, rec.Code, rec.Body, rec.Header().Get("Location"))
			}
		}
		if len(f.paged) != 0 {
			t.Errorf("hint %q: a page was worked out", hint)
		}
	}
}

func TestRefusalsSayNothingToTheVisitor(t *testing.T) {
	rec := ask(New(&fake{}), CheckPath, true, map[string]string{"X-Forwarded-Uri": "/", "X-Forwarded-Method": "GET /x"})
	if strings.Contains(rec.Body.String(), "X-Forwarded") || !strings.HasPrefix(rec.Header().Get(HeaderVerdict), "refused: ") {
		t.Errorf("body %q, header %q", rec.Body, rec.Header().Get(HeaderVerdict))
	}
}
