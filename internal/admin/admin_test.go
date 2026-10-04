package admin

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MaMoja/xibalba/internal/health"
)

const password = "correct horse battery"

var (
	hashOnce sync.Once
	testHash Hash
)

// The hash is costly on purpose; the tests share one.
func stored(t *testing.T) Hash {
	t.Helper()
	hashOnce.Do(func() {
		line, err := HashPassword(password)
		if err != nil {
			t.Fatal(err)
		}
		if testHash, err = ParseHash(line); err != nil {
			t.Fatal(err)
		}
	})
	return testHash
}

type world struct {
	t   *testing.T
	a   *Admin
	h   http.Handler
	now time.Time
	log bytes.Buffer
}

func newWorld(t *testing.T, change func(*Options)) *world {
	w := &world{t: t, now: time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC)}
	opts := Options{
		Password: stored(t), SessionLifetime: time.Hour, Version: "test",
		Now: func() time.Time { return w.now },
		Log: slog.New(slog.NewTextHandler(&w.log, nil)),
		Live: func() map[string]uint64 {
			return map[string]uint64{"decision|rule:block-admin|deny": 4, "decision|default|allow": 1234567}
		},
		Health: func() health.Report {
			return health.Report{State: health.OK, Components: map[string]health.Status{"rules": {State: health.OK}}}
		},
	}
	if change != nil {
		change(&opts)
	}
	a, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	w.a, w.h = a, a.Handler()
	return w
}

func (w *world) do(method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = "127.0.0.1:9091"
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, req)
	return rec
}

func (w *world) signIn(pw string, headers map[string]string) *httptest.ResponseRecorder {
	return w.do("POST", "/login", "password="+url.QueryEscape(pw), headers)
}

func cookieOf(rec *httptest.ResponseRecorder) string {
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			return c.Name + "=" + c.Value
		}
	}
	return ""
}

func TestPasswordHash(t *testing.T) {
	h := stored(t)
	if !h.Matches(password) || h.Matches(password+"x") || h.Matches("") || h.Matches(strings.Repeat("a", 5000)) {
		t.Error("Matches is wrong")
	}
	if (Hash{}).Matches("") {
		t.Error("an empty hash matches")
	}
	if _, err := HashPassword("short"); err == nil {
		t.Error("a short password was accepted")
	}
	a, _ := HashPassword(password)
	b, _ := HashPassword(password)
	if a == b {
		t.Error("two hashes of one password are equal: no salt")
	}
	for _, bad := range []string{"", "plain", "pbkdf2-sha256$10$AAAA$AAAA", "pbkdf2-sha256$600000$!!$!!",
		"bcrypt$600000$AAAAAAAAAAAAAAAAAAAAAA$AAAA", "pbkdf2-sha256$99999999999$AAAAAAAAAAAAAAAAAAAAAA$" + strings.Repeat("A", 43)} {
		if _, err := ParseHash(bad); err == nil {
			t.Errorf("ParseHash(%q) accepted", bad)
		} else if bad != "" && len(bad) > 8 && strings.Contains(err.Error(), bad) {
			t.Errorf("the error repeats the line: %v", err)
		}
	}
}

func TestNothingWithoutSignIn(t *testing.T) {
	w := newWorld(t, nil)
	if rec := w.do("GET", "/", "", nil); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Errorf("GET /: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	for _, forged := range []string{"xibalba_admin=", "xibalba_admin=guess", "xibalba_admin=" + strings.Repeat("A", 43)} {
		if rec := w.do("GET", "/", "", map[string]string{"Cookie": forged}); rec.Code != http.StatusSeeOther {
			t.Errorf("GET / with %q: %d", forged, rec.Code)
		}
	}
	if rec := w.do("GET", "/login", "", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), `type="password"`) ||
		strings.Contains(rec.Body.String(), "block-admin") {
		t.Errorf("GET /login: %d", rec.Code)
	}
	for _, target := range []string{"/assets/admin.go", "/assets/../admin.go", "/anything", "/assets/locales/en.json"} {
		if rec := w.do("GET", target, "", nil); rec.Code == 200 {
			t.Errorf("GET %s: 200", target)
		}
	}
}

func TestSignInOverviewSignOut(t *testing.T) {
	w := newWorld(t, nil)
	rec := w.signIn(password, map[string]string{"Origin": "http://127.0.0.1:9091"})
	cookie := cookieOf(rec)
	if rec.Code != http.StatusSeeOther || cookie == "" {
		t.Fatalf("sign-in: %d, cookie %q", rec.Code, cookie)
	}
	set := rec.Header().Get("Set-Cookie")
	if !strings.Contains(set, "HttpOnly") || !strings.Contains(set, "SameSite=Strict") {
		t.Errorf("cookie = %q", set)
	}
	page := w.do("GET", "/", "", map[string]string{"Cookie": cookie, "Accept-Language": "de"})
	body := page.Body.String()
	for _, want := range []string{`lang="de"`, "block-admin", "1.234.567", "seit dem letzten Start", "arbeitet"} {
		if !strings.Contains(body, want) {
			t.Errorf("overview lacks %q", want)
		}
	}
	if page.Header().Get("Content-Security-Policy") != csp || page.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers = %v", page.Header())
	}
	if strings.Contains(body, "<script") || strings.Contains(body, "style=") {
		t.Error("the page holds a script or an inline style")
	}

	// The session ends by itself.
	w.now = w.now.Add(2 * time.Hour)
	if rec := w.do("GET", "/", "", map[string]string{"Cookie": cookie}); rec.Code != http.StatusSeeOther {
		t.Errorf("after the session's end: %d", rec.Code)
	}

	// Signing out ends it at once.
	cookie = cookieOf(w.signIn(password, nil))
	if rec := w.do("POST", "/logout", "", map[string]string{"Cookie": cookie}); rec.Code != http.StatusSeeOther {
		t.Errorf("sign-out: %d", rec.Code)
	}
	if rec := w.do("GET", "/", "", map[string]string{"Cookie": cookie}); rec.Code != http.StatusSeeOther {
		t.Errorf("after signing out: %d", rec.Code)
	}

	if log := w.log.String(); strings.Contains(log, password) || strings.Contains(log, strings.TrimPrefix(cookie, cookieName+"=")) {
		t.Errorf("the log holds the password or a session:\n%s", log)
	}
}

func TestRequestsFromOtherSitesAreRefused(t *testing.T) {
	w := newWorld(t, nil)
	for _, headers := range []map[string]string{
		{"Origin": "https://evil.example"},
		{"Sec-Fetch-Site": "cross-site"},
		{"Sec-Fetch-Site": "same-site"},
		{"Origin": "null"},
	} {
		if rec := w.signIn(password, headers); rec.Code != http.StatusForbidden || cookieOf(rec) != "" {
			t.Errorf("sign-in with %v: %d", headers, rec.Code)
		}
		if rec := w.do("POST", "/logout", "", headers); rec.Code != http.StatusForbidden {
			t.Errorf("sign-out with %v: %d", headers, rec.Code)
		}
	}
	if rec := w.do("GET", "/logout", "", nil); rec.Code == http.StatusSeeOther {
		t.Error("sign-out by a link")
	}
}

func TestWrongPasswordsAreSlowedDown(t *testing.T) {
	w := newWorld(t, nil)
	for i := 0; i < freeFailures; i++ {
		if rec := w.signIn("wrong password "+string(rune('a'+i)), nil); rec.Code != http.StatusUnauthorized || cookieOf(rec) != "" {
			t.Fatalf("attempt %d: %d", i+1, rec.Code)
		}
	}
	// Now even the right password has to wait.
	rec := w.signIn(password, nil)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" || cookieOf(rec) != "" {
		t.Fatalf("after %d failures: %d", freeFailures, rec.Code)
	}
	w.now = w.now.Add(firstWait + time.Second)
	if rec := w.signIn("still wrong, sorry", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("after the wait: %d", rec.Code)
	}
	// The wait doubles.
	w.now = w.now.Add(firstWait + time.Second)
	if rec := w.signIn(password, nil); rec.Code != http.StatusTooManyRequests {
		t.Errorf("second wait is not longer: %d", rec.Code)
	}
	w.now = w.now.Add(maxWait)
	if rec := w.signIn(password, nil); rec.Code != http.StatusSeeOther {
		t.Errorf("right password after the wait: %d", rec.Code)
	}
	// A huge body is not read into a password.
	if rec := w.signIn(strings.Repeat("a", 1<<20), nil); rec.Code == http.StatusSeeOther {
		t.Error("a megabyte was accepted as a password")
	}
}

func TestTablesAreBounded(t *testing.T) {
	w := newWorld(t, func(o *Options) { o.Password = Hash{} }) // every check fails at once
	for i := 0; i < maxThrottled+50; i++ {
		req := httptest.NewRequest("POST", "/login", strings.NewReader("password=x"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "10." + itoa(i/65536) + "." + itoa(i/256%256) + "." + itoa(i%256) + ":1"
		w.h.ServeHTTP(httptest.NewRecorder(), req)
	}
	if n := len(w.a.failures); n > maxThrottled+1 {
		t.Errorf("%d addresses remembered", n)
	}
}

func itoa(i int) string {
	return string(rune('0'+i/100%10)) + string(rune('0'+i/10%10)) + string(rune('0'+i%10))
}

func TestOverviewWithHistory(t *testing.T) {
	var asked [2]time.Time
	w := newWorld(t, func(o *Options) {
		o.DryRun = true
		o.History = func(from, to time.Time) []Hour {
			asked = [2]time.Time{from, to}
			return []Hour{
				{Start: to, Counts: map[string]uint64{
					"decision|default|allow": 90, "decision|rule:<script>alert(1)</script>|deny": 7, "decision|rule:a|b|challenge": 3,
					"crawler|GPTBot|verified": 5, "crawler|GPTBot|impostor": 2, "crawler|Quiet|pending": 0,
					"challenge|served": 3, "limit|1m0s|requests|challenge": 11, "trap|hits": 1,
				}},
				{Start: to.Add(-48 * time.Hour), Counts: map[string]uint64{"decision|default|allow": 1000}}, // outside: ignored
			}
		}
	})
	cookie := cookieOf(w.signIn(password, nil))
	rec := w.do("GET", "/?range=day", "", map[string]string{"Cookie": cookie})
	body := rec.Body.String()
	if got := asked[1].Sub(asked[0]); got != 23*time.Hour {
		t.Errorf("asked for %s", got)
	}
	for _, want := range []string{"<svg", `class="allow"`, `class="deny"`, "&lt;script&gt;alert(1)", "a|b", "GPTBot", "Dry run", ">90<", "1m0s", `aria-current="page"`} {
		if !strings.Contains(body, want) {
			t.Errorf("overview lacks %q", want)
		}
	}
	for _, not := range []string{"<script>alert", "Quiet", "1,090"} {
		if strings.Contains(body, not) {
			t.Errorf("overview holds %q", not)
		}
	}
	if rec := w.do("GET", "/?range=month", "", map[string]string{"Cookie": cookie}); rec.Code != 200 || asked[1].Sub(asked[0]) < 29*24*time.Hour {
		t.Errorf("month: %d, asked for %s", rec.Code, asked[1].Sub(asked[0]))
	}
	if rec := w.do("GET", "/?range=%00<x>", "", map[string]string{"Cookie": cookie}); rec.Code != 200 || strings.Contains(rec.Body.String(), "<x>") {
		t.Errorf("unknown range: %d", rec.Code)
	}
}

func TestNumber(t *testing.T) {
	for _, tt := range []struct {
		v       uint64
		lang, s string
	}{{0, "en", "0"}, {999, "de", "999"}, {1000, "de", "1.000"}, {1234567, "en", "1,234,567"}} {
		if got := number(tt.v, tt.lang); got != tt.s {
			t.Errorf("number(%d, %s) = %q", tt.v, tt.lang, got)
		}
	}
}
