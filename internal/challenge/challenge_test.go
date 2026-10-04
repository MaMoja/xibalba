package challenge

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MaMoja/xibalba/internal/clientip"
	"github.com/MaMoja/xibalba/internal/token"
)

const browser = "Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0"

// solve does what the page's JavaScript does: try numbers until one works.
func solve(nonce string, difficulty int) string {
	for n := 0; ; n++ {
		if s := strconv.Itoa(n); SolvesPoW(nonce, s, difficulty) {
			return s
		}
	}
}

// site is a Challenge in front of a fake website, wired as in the real program.
type site struct {
	want    *Profile // the check the site's rule asks for; nil is the default
	t       *testing.T
	c       *Challenge
	handler http.Handler
	views   []View // every challenge page shown, in order
	mu      sync.Mutex
	now     time.Time
}

func newSite(t *testing.T, change func(*Options)) *site {
	t.Helper()
	key := make([]byte, token.KeySize)
	signer, err := token.NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	s := &site{t: t, now: time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)}
	opts := Options{
		Signer:            signer,
		Default:           Profile{Method: MethodPoW, Difficulty: MinDifficulty, AllowButton: true, Wait: 3 * time.Second},
		ChallengeLifetime: 5 * time.Minute,
		PassLifetime:      24 * time.Hour,
		BindNetwork:       true,
		CookieName:        "xibalba-pass",
		Page: func(w http.ResponseWriter, _ *http.Request, v View) {
			s.views = append(s.views, v)
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, "challenge page")
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { s.mu.Lock(); defer s.mu.Unlock(); return s.now },
	}
	if change != nil {
		change(&opts)
	}
	s.c = New(opts)

	website := s.c.StripPass(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "website; cookies: "+r.Header.Get("Cookie"))
	}))
	router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, Prefix):
			s.c.Handler().ServeHTTP(w, r)
		case s.c.Passed(r, s.want):
			website.ServeHTTP(w, r)
		default:
			s.c.Serve(w, r, s.want)
		}
	})
	s.handler = clientip.Middleware(clientip.New([]netip.Prefix{netip.MustParsePrefix("10.0.0.1/32")}), router)
	return s
}

func (s *site) advance(d time.Duration) { s.mu.Lock(); s.now = s.now.Add(d); s.mu.Unlock() }

// client is one visitor: an address, a user agent and a cookie jar.
type client struct {
	s       *site
	remote  string
	agent   string
	cookies []*http.Cookie
	headers map[string]string
}

func (s *site) client(remote, agent string) *client {
	return &client{s: s, remote: remote, agent: agent}
}

func (c *client) do(method, target string, form url.Values) *httptest.ResponseRecorder {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, target, body)
	req.RemoteAddr = c.remote
	req.Header.Set("User-Agent", c.agent)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	for _, ck := range c.cookies {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	c.s.handler.ServeHTTP(rec, req)
	for _, fresh := range rec.Result().Cookies() { // a browser keeps one cookie per name
		kept := c.cookies[:0]
		for _, old := range c.cookies {
			if old.Name != fresh.Name {
				kept = append(kept, old)
			}
		}
		c.cookies = append(kept, fresh)
	}
	return rec
}

func (c *client) get(target string) *httptest.ResponseRecorder {
	return c.do(http.MethodGet, target, nil)
}

// challenge requests target, expects a challenge page and returns its task.
func (c *client) challenge(target string) View {
	c.s.t.Helper()
	before := len(c.s.views)
	if rec := c.get(target); rec.Code != http.StatusForbidden || len(c.s.views) != before+1 {
		c.s.t.Fatalf("GET %s: status %d, %d new challenge pages; want a challenge", target, rec.Code, len(c.s.views)-before)
	}
	return c.s.views[len(c.s.views)-1]
}

func answer(v View, method, solution string) url.Values {
	return url.Values{"token": {v.Token}, "return": {v.Return}, "method": {method}, "solution": {solution}}
}

func TestProofOfWorkFlow(t *testing.T) {
	s := newSite(t, nil)
	c := s.client("203.0.113.5:40000", browser)

	v := c.challenge("/wiki/page?id=7")
	if v.Action != VerifyPath || v.Return != "/wiki/page?id=7" || v.Difficulty != MinDifficulty || len(v.Nonce) != 32 {
		t.Fatalf("challenge view = %+v", v)
	}

	rec := c.do(http.MethodPost, VerifyPath, answer(v, MethodPoW, solve(v.Nonce, v.Difficulty)))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/wiki/page?id=7" {
		t.Fatalf("after a correct answer: status %d, Location %q; want 303 back to the page", rec.Code, rec.Header().Get("Location"))
	}
	if len(c.cookies) != 1 {
		t.Fatalf("got %d cookies, want the pass", len(c.cookies))
	}
	pass := c.cookies[0]
	if pass.Name != "xibalba-pass" || !pass.HttpOnly || pass.SameSite != http.SameSiteLaxMode || pass.Path != "/" || pass.MaxAge != 86400 {
		t.Errorf("pass cookie attributes: %+v", pass)
	}

	if rec := c.get("/wiki/page?id=7"); rec.Code != http.StatusOK {
		t.Errorf("with the pass: status %d, want 200", rec.Code)
	}
	if solved, failed := s.c.Counts(); solved != 1 || failed != 0 {
		t.Errorf("counts: solved %d, failed %d", solved, failed)
	}
}

func TestButtonFlow(t *testing.T) {
	s := newSite(t, nil)
	c := s.client("203.0.113.5:40000", "NoScriptBrowser/1.0")
	v := c.challenge("/")

	// Pressed at once: too early. The client gets a new task and a note.
	rec := c.do(http.MethodPost, VerifyPath, answer(v, answerButton, ""))
	if rec.Code != http.StatusForbidden || len(c.cookies) != 0 {
		t.Fatalf("early press: status %d, %d cookies; want another challenge and no pass", rec.Code, len(c.cookies))
	}
	again := s.views[len(s.views)-1]
	if again.Message != MessageTooEarly || again.Token == v.Token {
		t.Errorf("after an early press: message %q, same token %v", again.Message, again.Token == v.Token)
	}

	s.advance(4 * time.Second)
	rec = c.do(http.MethodPost, VerifyPath, answer(again, answerButton, ""))
	if rec.Code != http.StatusSeeOther || len(c.cookies) != 1 {
		t.Fatalf("after waiting: status %d, %d cookies; want the pass", rec.Code, len(c.cookies))
	}
	if rec := c.get("/"); rec.Code != http.StatusOK {
		t.Errorf("with the pass: status %d", rec.Code)
	}
}

func TestButtonCanBeSwitchedOff(t *testing.T) {
	s := newSite(t, func(o *Options) { o.Default.AllowButton = false })
	c := s.client("203.0.113.5:40000", browser)
	v := c.challenge("/")
	if v.AllowButton {
		t.Error("the page offers the button although it is switched off")
	}
	s.advance(10 * time.Second)
	if rec := c.do(http.MethodPost, VerifyPath, answer(v, answerButton, "")); rec.Code != http.StatusForbidden || len(c.cookies) != 0 {
		t.Errorf("button answer accepted although switched off: status %d", rec.Code)
	}
}

func TestRejectedAnswers(t *testing.T) {
	tests := []struct {
		name string
		make func(s *site, c *client, v View) url.Values
	}{
		{"wrong solution", func(_ *site, _ *client, v View) url.Values { return answer(v, MethodPoW, "not-a-number") }},
		{"empty solution", func(_ *site, _ *client, v View) url.Values { return answer(v, MethodPoW, "") }},
		{"unknown method", func(_ *site, _ *client, v View) url.Values { return answer(v, "magic", "1") }},
		{"no method", func(_ *site, _ *client, v View) url.Values { return answer(v, "", solve(v.Nonce, v.Difficulty)) }},
		{"no token", func(_ *site, _ *client, v View) url.Values {
			f := answer(v, MethodPoW, solve(v.Nonce, v.Difficulty))
			f.Del("token")
			return f
		}},
		{"token changed", func(_ *site, _ *client, v View) url.Values {
			f := answer(v, MethodPoW, solve(v.Nonce, v.Difficulty))
			f.Set("token", v.Token[:len(v.Token)-2]+"xx")
			return f
		}},
		{"task expired", func(s *site, _ *client, v View) url.Values {
			s.advance(6 * time.Minute)
			return answer(v, MethodPoW, solve(v.Nonce, v.Difficulty))
		}},
		{"a pass used as a task", func(s *site, _ *client, v View) url.Values {
			f := answer(v, MethodPoW, solve(v.Nonce, v.Difficulty))
			f.Set("token", s.c.opts.Signer.Sign(token.Pass, token.Claims{Expires: s.now.Add(time.Hour)}))
			return f
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSite(t, nil)
			c := s.client("203.0.113.5:40000", browser)
			v := c.challenge("/page")
			rec := c.do(http.MethodPost, VerifyPath, tt.make(s, c, v))
			if rec.Code != http.StatusForbidden || len(c.cookies) != 0 {
				t.Fatalf("status %d, %d cookies; want a new challenge and no pass", rec.Code, len(c.cookies))
			}
			last := s.views[len(s.views)-1]
			if last.Message != MessageRetry || last.Token == v.Token || last.Return != "/page" {
				t.Errorf("new challenge = %+v; want a fresh task, the retry note and the same return address", last)
			}
			if _, failed := s.c.Counts(); failed != 1 {
				t.Errorf("failed count = %d, want 1", failed)
			}
			// The client is not stuck: the new task can be solved.
			if rec := c.do(http.MethodPost, VerifyPath, answer(last, MethodPoW, solve(last.Nonce, last.Difficulty))); rec.Code != http.StatusSeeOther {
				t.Errorf("solving the new task: status %d, want 303", rec.Code)
			}
		})
	}
}

// A task or a pass belongs to the client it was issued to.
func TestTokensAreBoundToTheClient(t *testing.T) {
	t.Run("task solved by another machine", func(t *testing.T) {
		s := newSite(t, nil)
		a := s.client("203.0.113.5:40000", browser)
		b := s.client("198.51.100.9:40000", browser)
		v := a.challenge("/")
		if rec := b.do(http.MethodPost, VerifyPath, answer(v, MethodPoW, solve(v.Nonce, v.Difficulty))); rec.Code != http.StatusForbidden || len(b.cookies) != 0 {
			t.Errorf("another network redeemed the task: status %d", rec.Code)
		}
	})

	pass := func(s *site, remote, agent string) *http.Cookie {
		c := s.client(remote, agent)
		v := c.challenge("/")
		c.do(http.MethodPost, VerifyPath, answer(v, MethodPoW, solve(v.Nonce, v.Difficulty)))
		if len(c.cookies) != 1 {
			t.Fatal("could not obtain a pass")
		}
		return c.cookies[0]
	}

	tests := []struct {
		name        string
		bindNetwork bool
		remote      string
		agent       string
		want        int
	}{
		{"same client", true, "203.0.113.5:40001", browser, 200},
		{"same network, other address", true, "203.0.113.200:1", browser, 200},
		{"other network", true, "198.51.100.9:1", browser, 403},
		{"other user agent", true, "203.0.113.5:1", "curl/8.0", 403},
		{"other network, network binding off", false, "198.51.100.9:1", browser, 200},
		{"other user agent, network binding off", false, "203.0.113.5:1", "curl/8.0", 403},
	}
	for _, tt := range tests {
		t.Run("pass used by "+tt.name, func(t *testing.T) {
			s := newSite(t, func(o *Options) { o.BindNetwork = tt.bindNetwork })
			c := s.client(tt.remote, tt.agent)
			c.cookies = []*http.Cookie{pass(s, "203.0.113.5:40000", browser)}
			if rec := c.get("/"); rec.Code != tt.want {
				t.Errorf("status %d, want %d", rec.Code, tt.want)
			}
		})
	}

	t.Run("ipv6 clients are bound to their /64", func(t *testing.T) {
		s := newSite(t, nil)
		c := s.client("[2001:db8:1:2::10]:1", browser)
		c.cookies = []*http.Cookie{pass(s, "[2001:db8:1:2:aaaa::1]:1", browser)}
		if rec := c.get("/"); rec.Code != 200 {
			t.Errorf("same /64: status %d, want 200", rec.Code)
		}
		c.remote = "[2001:db8:1:3::10]:1"
		if rec := c.get("/"); rec.Code != 403 {
			t.Errorf("other /64: status %d, want 403", rec.Code)
		}
	})
}

func TestPassExpiresAndCannotBeForged(t *testing.T) {
	s := newSite(t, nil)
	c := s.client("203.0.113.5:40000", browser)
	v := c.challenge("/")
	c.do(http.MethodPost, VerifyPath, answer(v, MethodPoW, solve(v.Nonce, v.Difficulty)))
	good := c.cookies[0].Value

	for name, value := range map[string]string{
		"empty":                "",
		"garbage":              "let-me-in",
		"last character wrong": good[:len(good)-1] + "A",
		"a task as a pass":     v.Token,
	} {
		c.cookies = []*http.Cookie{{Name: "xibalba-pass", Value: value}}
		if rec := c.get("/"); rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want a challenge", name, rec.Code)
		}
	}

	c.cookies = []*http.Cookie{{Name: "xibalba-pass", Value: good}}
	s.advance(23 * time.Hour)
	if rec := c.get("/"); rec.Code != http.StatusOK {
		t.Errorf("within the lifetime: status %d", rec.Code)
	}
	s.advance(2 * time.Hour)
	if rec := c.get("/"); rec.Code != http.StatusForbidden {
		t.Errorf("after the lifetime: status %d, want a new challenge", rec.Code)
	}
}

func TestSecureCookieFollowsHTTPS(t *testing.T) {
	tests := []struct {
		name   string
		remote string
		proto  string
		want   bool
	}{
		{"plain http", "203.0.113.5:1", "", false},
		{"https reported by the trusted proxy", "10.0.0.1:1", "https", true},
		{"http reported by the trusted proxy", "10.0.0.1:1", "http", false},
		{"https claimed by an untrusted client", "203.0.113.5:1", "https", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSite(t, func(o *Options) { o.BindNetwork = false })
			c := s.client(tt.remote, browser)
			if tt.proto != "" {
				c.headers = map[string]string{"X-Forwarded-Proto": tt.proto}
			}
			v := c.challenge("/")
			c.do(http.MethodPost, VerifyPath, answer(v, MethodPoW, solve(v.Nonce, v.Difficulty)))
			if len(c.cookies) != 1 || c.cookies[0].Secure != tt.want {
				t.Errorf("Secure = %v, want %v", len(c.cookies) == 1 && c.cookies[0].Secure, tt.want)
			}
		})
	}
}

func TestVerifyEndpointEdges(t *testing.T) {
	s := newSite(t, nil)
	c := s.client("203.0.113.5:40000", browser)

	if rec := c.get(VerifyPath); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Errorf("GET verify: status %d, Location %q; want 303 to /", rec.Code, rec.Header().Get("Location"))
	}
	if rec := c.get(Prefix + "something-else"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown path under the prefix: status %d, want 404", rec.Code)
	}
	huge := url.Values{"token": {strings.Repeat("a", 64<<10)}}
	if rec := c.do(http.MethodPost, VerifyPath, huge); rec.Code != http.StatusBadRequest {
		t.Errorf("oversized form: status %d, want 400", rec.Code)
	}
	if len(c.cookies) != 0 {
		t.Error("a cookie was set by one of these requests")
	}
}

// The address a client is sent on to must always be on this website.
func TestSafeReturn(t *testing.T) {
	keep := []string{"/", "/wiki/page", "/search?q=a%20b&x=1", "/a/b/?next=/c", "/path#section", "/über", "/a:b", "/.well-known/x"}
	for _, target := range keep {
		if got := SafeReturn(target); got != target {
			t.Errorf("SafeReturn(%q) = %q, want it kept", target, got)
		}
	}
	replace := []string{
		"", "relative/path", "https://evil.example/", "http://evil.example", "//evil.example", "//evil.example/path",
		`/\evil.example`, `\\evil.example`, "/%2F%2Fevil.example", "javascript:alert(1)", "/ok\r\nSet-Cookie: x=1",
		"/tab\there", "/\x00", "///evil.example", "/" + strings.Repeat("a", maxReturnLen),
		VerifyPath, Prefix + "x", "/.xibalba", "/%2Exibalba/verify",
	}
	for _, target := range replace {
		if got := SafeReturn(target); got != "/" {
			t.Errorf("SafeReturn(%q) = %q, want /", target, got)
		}
	}
}

func TestOpenRedirectThroughTheForm(t *testing.T) {
	s := newSite(t, nil)
	c := s.client("203.0.113.5:40000", browser)
	v := c.challenge("/")
	form := answer(v, MethodPoW, solve(v.Nonce, v.Difficulty))
	form.Set("return", "https://evil.example/phish")
	rec := c.do(http.MethodPost, VerifyPath, form)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Errorf("status %d, Location %q; want a redirect to / only", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSolvesPoW(t *testing.T) {
	nonce := "00112233445566778899aabbccddeeff"
	good := solve(nonce, 12)
	tests := []struct {
		name       string
		nonce      string
		solution   string
		difficulty int
		want       bool
	}{
		{"correct", nonce, good, 12, true},
		{"correct answer for an easier task", nonce, good, 8, true},
		{"wrong number", nonce, "1", 20, false},
		{"other nonce", "ffeeddccbbaa99887766554433221100", good, 12, false},
		{"empty", nonce, "", 12, false},
		{"empty nonce", "", good, 12, false},
		{"sign", nonce, "+" + good, 12, false},
		{"negative", nonce, "-1", 12, false},
		{"space", nonce, " " + good, 12, false},
		{"hexadecimal", nonce, "0x1f", 12, false},
		{"exponent", nonce, "1e9", 12, false},
		{"other digits", nonce, "١٢٣", 12, false},
		{"too long", nonce, strings.Repeat("9", maxSolutionLen+1), 12, false},
		{"difficulty zero accepts nothing", nonce, good, 0, false},
		{"difficulty negative", nonce, good, -5, false},
		{"difficulty above the maximum", nonce, good, MaxDifficulty + 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SolvesPoW(tt.nonce, tt.solution, tt.difficulty); got != tt.want {
				t.Errorf("SolvesPoW = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStripPass(t *testing.T) {
	tests := map[string]string{
		"xibalba-pass=abc":                          "",
		"session=1; xibalba-pass=abc":               "session=1",
		"xibalba-pass=abc; session=1":               "session=1",
		"a=1; xibalba-pass=abc; b=\"quoted value\"": "a=1; b=\"quoted value\"",
		"session=1": "session=1",
		"not-xibalba-pass=1; xibalba-pass-other=2": "not-xibalba-pass=1; xibalba-pass-other=2",
		"a=xibalba-pass":                             "a=xibalba-pass",
		"a=1;xibalba-pass=abc;b=2":                   "a=1; b=2",
		"xibalba-pass=abc; xibalba-pass=second-copy": "",
	}
	s := newSite(t, nil)
	for in, want := range tests {
		var got string
		h := s.c.StripPass(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.Header.Get("Cookie") }))
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Cookie", in)
		h.ServeHTTP(httptest.NewRecorder(), req)
		if got != want {
			t.Errorf("Cookie %q became %q, want %q", in, got, want)
		}
	}
}

func TestWebsiteNeverSeesThePass(t *testing.T) {
	s := newSite(t, nil)
	c := s.client("203.0.113.5:40000", browser)
	v := c.challenge("/")
	c.do(http.MethodPost, VerifyPath, answer(v, MethodPoW, solve(v.Nonce, v.Difficulty)))
	c.cookies = append(c.cookies, &http.Cookie{Name: "session", Value: "abc"})
	rec := c.get("/")
	if body := rec.Body.String(); body != "website; cookies: session=abc" {
		t.Errorf("website received %q, want only its own cookie", body)
	}
}

func TestEachChallengeIsDifferent(t *testing.T) {
	s := newSite(t, nil)
	c := s.client("203.0.113.5:40000", browser)
	a, b := c.challenge("/"), c.challenge("/")
	if a.Nonce == b.Nonce || a.Token == b.Token {
		t.Error("two challenges share a nonce or a token")
	}
}

// answer sends the form of a challenge page back.
func (c *client) answer(v View, fields url.Values) *httptest.ResponseRecorder {
	form := url.Values{"token": {v.Token}, "return": {v.Return}}
	for k, vals := range fields {
		form[k] = vals
	}
	return c.do(http.MethodPost, VerifyPath, form)
}

func passed(rec *httptest.ResponseRecorder) bool { return rec.Code == http.StatusSeeOther }

func TestMethodScript(t *testing.T) {
	s := newSite(t, func(o *Options) { o.Default = Profile{Method: MethodScript, Wait: 2 * time.Second} })
	c := s.client("192.0.2.1:1000", "Mozilla/5.0")
	v := c.challenge("/")
	if v.Method != MethodScript || v.WaitSeconds != 2 || v.AllowButton {
		t.Fatalf("view = %+v", v)
	}
	good := url.Values{"method": {MethodScript}, "solution": {scriptAnswer(v.Nonce)}}
	if rec := c.answer(v, good); passed(rec) || s.views[len(s.views)-1].Message != MessageTooEarly {
		t.Fatalf("answered before the wait was over: %d", rec.Code)
	}
	v = c.challenge("/")
	s.advance(3 * time.Second)
	for name, fields := range map[string]url.Values{
		"wrong value":           {"method": {MethodScript}, "solution": {"00000000"}},
		"no value":              {"method": {MethodScript}},
		"the button":            {"method": {answerButton}},
		"proof of work instead": {"method": {MethodPoW}, "solution": {"1"}},
		"no method":             {},
	} {
		if rec := c.answer(v, fields); passed(rec) {
			t.Errorf("%s was accepted", name)
		}
	}
	if rec := c.answer(v, url.Values{"method": {MethodScript}, "solution": {scriptAnswer(v.Nonce)}}); !passed(rec) {
		t.Fatalf("the right answer after the wait: %d", rec.Code)
	}
	if rec := c.get("/"); rec.Code != 200 {
		t.Errorf("with the pass: %d", rec.Code)
	}
}

func TestMethodsWaitAndRefresh(t *testing.T) {
	for _, method := range []string{MethodWait, MethodRefresh} {
		s := newSite(t, func(o *Options) { o.Default = Profile{Method: method, Wait: 2 * time.Second} })
		c := s.client("192.0.2.1:1000", "Mozilla/5.0")
		v := c.challenge("/page?x=1")
		if !v.AllowButton || (method == MethodRefresh) != (v.RefreshURL != "") {
			t.Fatalf("%s: view = %+v", method, v)
		}
		if rec := c.answer(v, url.Values{"method": {answerButton}}); passed(rec) {
			t.Errorf("%s: the button counted before the wait was over", method)
		}
		v = c.challenge("/page?x=1")
		s.advance(2 * time.Second)
		if method == MethodRefresh {
			// The browser follows the page's own forward: a GET.
			if !strings.HasPrefix(v.RefreshURL, VerifyPath+"?") {
				t.Fatalf("refresh address = %q", v.RefreshURL)
			}
			rec := c.get(v.RefreshURL)
			if !passed(rec) || rec.Header().Get("Location") != "/page?x=1" {
				t.Fatalf("refresh: %d to %q", rec.Code, rec.Header().Get("Location"))
			}
		} else {
			// A GET is only for the method refresh.
			q := url.Values{"token": {v.Token}, "return": {v.Return}}
			if rec := c.get(VerifyPath + "?" + q.Encode()); passed(rec) && len(rec.Result().Cookies()) > 0 {
				t.Errorf("wait: a GET earned a pass")
			}
			v = c.challenge("/page?x=1")
			s.advance(2 * time.Second)
			if rec := c.answer(v, url.Values{"method": {answerButton}}); !passed(rec) {
				t.Fatalf("wait: the button after the wait: %d", rec.Code)
			}
		}
		if rec := c.get("/page?x=1"); rec.Code != 200 {
			t.Errorf("%s: with the pass: %d", method, rec.Code)
		}
	}
	// A GET without a task, or with a forged one, is sent home or asked again.
	s := newSite(t, nil)
	c := s.client("192.0.2.1:1000", "Mozilla/5.0")
	if rec := c.get(VerifyPath); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" || len(rec.Result().Cookies()) != 0 {
		t.Errorf("GET without a task: %d", rec.Code)
	}
	if rec := c.get(VerifyPath + "?token=forged&return=/x"); passed(rec) {
		t.Errorf("GET with a forged task: %d", rec.Code)
	}
}

func TestExtraChecks(t *testing.T) {
	s := newSite(t, func(o *Options) {
		o.Default = Profile{Method: MethodPoW, Difficulty: MinDifficulty, Wait: time.Second, AllowButton: true, Checks: []string{CheckCSS, CheckHeadless}}
	})
	const agent = "Mozilla/5.0 (X11; Linux x86_64)"
	c := s.client("192.0.2.1:1000", agent)
	v := c.challenge("/")
	// Extra checks need the script, so the path without it is closed.
	if v.AllowButton || v.StyleURL == "" || !v.Headless {
		t.Fatalf("view = %+v", v)
	}
	// The style sheet holds the value; another task's value does not fit.
	sheet := c.get(v.StyleURL)
	m := regexp.MustCompile(`--xibalba-check:"([0-9a-f]+)"`).FindStringSubmatch(sheet.Body.String())
	if sheet.Code != 200 || m == nil || !strings.HasPrefix(sheet.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("style sheet: %d %q", sheet.Code, sheet.Body)
	}
	other := c.get(CSSPath + "?n=another")
	if strings.Contains(other.Body.String(), m[1]) {
		t.Error("two tasks share a style value")
	}
	for _, bad := range []string{CSSPath, CSSPath + "?n=" + strings.Repeat("a", 100)} {
		if rec := c.get(bad); rec.Code == 200 {
			t.Errorf("GET %s: 200", bad)
		}
	}

	solve := func(v View) string {
		for n := 0; ; n++ {
			if SolvesPoW(v.Nonce, strconv.Itoa(n), v.Difficulty) {
				return strconv.Itoa(n)
			}
		}
	}
	full := func(v View) url.Values {
		sheet := c.get(v.StyleURL)
		value := regexp.MustCompile(`--xibalba-check:"([0-9a-f]+)"`).FindStringSubmatch(sheet.Body.String())[1]
		return url.Values{"method": {MethodPoW}, "solution": {solve(v)}, "css": {value}, "probe": {"ok"}, "agent": {agent}}
	}
	without := func(v View, drop string, set ...string) url.Values {
		f := full(v)
		f.Del(drop)
		if len(set) == 1 {
			f.Set(drop, set[0])
		}
		return f
	}
	for name, change := range map[string]func(View) url.Values{
		"no style value":           func(v View) url.Values { return without(v, "css") },
		"wrong style value":        func(v View) url.Values { return without(v, "css", "abcdef") },
		"no report":                func(v View) url.Values { return without(v, "probe") },
		"report of automation":     func(v View) url.Values { return without(v, "probe", "webdriver") },
		"another browser's name":   func(v View) url.Values { return without(v, "agent", "Mozilla/5.0 HeadlessChrome") },
		"no browser name":          func(v View) url.Values { return without(v, "agent") },
		"the button":               func(v View) url.Values { return url.Values{"method": {answerButton}} },
		"right checks, wrong work": func(v View) url.Values { return without(v, "solution", "x") },
	} {
		v := c.challenge("/")
		s.advance(2 * time.Second)
		if rec := c.answer(v, change(v)); passed(rec) {
			t.Errorf("%s was accepted", name)
		}
	}
	if s.c.Automated() != 4 {
		t.Errorf("automated = %d, want 4", s.c.Automated())
	}
	v = c.challenge("/")
	if rec := c.answer(v, full(v)); !passed(rec) {
		t.Fatalf("everything right: %d", rec.Code)
	}
	if rec := c.get("/"); rec.Code != 200 {
		t.Errorf("with the pass: %d", rec.Code)
	}
}

// What the form says about the kind of check counts for nothing: the kind
// is in the signed task. And a pass counts where the same or less is asked.
func TestAPassCountsForWhatItWasEarnedWith(t *testing.T) {
	easy := &Profile{Method: MethodWait, Wait: time.Second}
	script := &Profile{Method: MethodScript, Wait: time.Second}
	hard := &Profile{Method: MethodPoW, Difficulty: 12, Wait: time.Second}
	harder := &Profile{Method: MethodPoW, Difficulty: 14, Wait: time.Second}
	probing := &Profile{Method: MethodScript, Wait: time.Second, Checks: []string{CheckHeadless}}

	s := newSite(t, nil)
	c := s.client("192.0.2.1:1000", "Mozilla/5.0")

	// A task for the proof of work is not answered by waiting, whatever the form says.
	s.want = hard
	v := c.challenge("/")
	s.advance(2 * time.Second)
	for _, claim := range []string{answerButton, MethodWait, MethodRefresh, MethodScript} {
		if rec := c.answer(v, url.Values{"method": {claim}, "solution": {scriptAnswer(v.Nonce)}}); passed(rec) {
			t.Errorf("a proof-of-work task was passed as %q", claim)
		}
	}

	// Earn the easy pass.
	s.want = easy
	v = c.challenge("/")
	s.advance(2 * time.Second)
	if rec := c.answer(v, url.Values{"method": {answerButton}}); !passed(rec) {
		t.Fatal("the easy check was not passed")
	}
	holds := func(p *Profile) bool { s.want = p; return c.get("/").Code == 200 }
	if !holds(easy) || holds(script) || holds(hard) || holds(probing) {
		t.Errorf("the easy pass: easy %v, script %v, hard %v, probing %v", holds(easy), holds(script), holds(hard), holds(probing))
	}

	// Earn the hard one: it also counts for everything below it, but not above, and not for extra checks.
	s.want = hard
	v = c.challenge("/")
	n := 0
	for !SolvesPoW(v.Nonce, strconv.Itoa(n), v.Difficulty) {
		n++
	}
	if rec := c.answer(v, url.Values{"method": {MethodPoW}, "solution": {strconv.Itoa(n)}}); !passed(rec) {
		t.Fatal("the hard check was not passed")
	}
	if !holds(easy) || !holds(script) || !holds(hard) || holds(harder) || holds(probing) {
		t.Error("the hard pass does not count as it should")
	}

	// Earn the probing one: the hard one is kept, not replaced.
	s.want = probing
	v = c.challenge("/")
	s.advance(2 * time.Second)
	if rec := c.answer(v, url.Values{"method": {MethodScript}, "solution": {scriptAnswer(v.Nonce)}, "probe": {"ok"}, "agent": {"Mozilla/5.0"}}); !passed(rec) {
		t.Fatal("the probing check was not passed")
	}
	if !holds(probing) || !holds(hard) || !holds(easy) || holds(harder) {
		t.Error("after the second check the first pass was lost, or more was gained than earned")
	}
}
