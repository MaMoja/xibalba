package admin

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MaMoja/xibalba/data"
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
		req.Host = "localhost:9091"
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

// Attempts sent side by side are counted before they are checked: no more
// than the free ones get a look at the password.
func TestParallelAttemptsCannotSlipUnderTheLimit(t *testing.T) {
	w := newWorld(t, nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[int]int{}
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := w.signIn("wrong and parallel", nil)
			mu.Lock()
			codes[rec.Code]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if codes[http.StatusUnauthorized] > freeFailures || codes[http.StatusUnauthorized]+codes[http.StatusTooManyRequests] != 30 {
		t.Errorf("answers = %v", codes)
	}
}

// A page of another site that points its own name at the listener gets no
// answer, whatever it claims to come from.
func TestOnlyKnownNamesAreAnswered(t *testing.T) {
	w := newWorld(t, func(o *Options) { o.Hosts = []string{"Xibalba.Example.org"} })
	for host, want := range map[string]int{
		"127.0.0.1:9091": 200, "localhost": 200, "[::1]:9091": 200, "xibalba.example.org": 200, "XIBALBA.example.org:443": 200,
		"evil.example:9091": 421, "127.0.0.1.evil.example": 421, "": 421, "10.0.0.5:9091": 421,
	} {
		req := httptest.NewRequest("GET", "/login", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		w.h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Host %q: %d, want %d", host, rec.Code, want)
		}
	}
	req := httptest.NewRequest("POST", "/login", strings.NewReader("password="+url.QueryEscape(password)))
	req.Host = "evil.example:9091"
	req.Header.Set("Origin", "http://evil.example:9091")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMisdirectedRequest || len(rec.Result().Cookies()) != 0 {
		t.Errorf("sign-in under a foreign name: %d", rec.Code)
	}
}

func TestSecureCookie(t *testing.T) {
	w := newWorld(t, func(o *Options) { o.SecureCookie = true })
	rec := w.signIn(password, nil)
	set := rec.Header().Get("Set-Cookie")
	if !strings.HasPrefix(set, "__Host-"+cookieName+"=") || !strings.Contains(set, "Secure") {
		t.Fatalf("cookie = %q", set)
	}
	c := rec.Result().Cookies()[0]
	if page := w.do("GET", "/", "", map[string]string{"Cookie": c.Name + "=" + c.Value}); page.Code != 200 {
		t.Errorf("overview with the secure cookie: %d", page.Code)
	}
}

type fakeSettings struct {
	presets   []Preset
	addresses []Address
	calls     []string
	fail      error
	rules     string
	versions  []Version
}

func (f *fakeSettings) Presets() []Preset    { return f.presets }
func (f *fakeSettings) Addresses() []Address { return f.addresses }
func (f *fakeSettings) SetPreset(name string, on bool) error {
	f.calls = append(f.calls, fmt.Sprintf("preset %s %v", name, on))
	return f.fail
}
func (f *fakeSettings) AddAddress(network, action, note string, lifetime time.Duration) error {
	f.calls = append(f.calls, fmt.Sprintf("add %s %s %q %s", network, action, note, lifetime))
	return f.fail
}
func (f *fakeSettings) RemoveAddress(network string) error {
	f.calls = append(f.calls, "remove "+network)
	return f.fail
}

type keyedError struct{ key string }

func (k keyedError) Error() string      { return "english text of " + k.key }
func (k keyedError) ProblemKey() string { return k.key }

var formValue = regexp.MustCompile(`name="form" value="([^"]+)"`)

func TestWithoutPermissionNothingCanBeChanged(t *testing.T) {
	w := newWorld(t, nil) // Settings is nil: the interface only shows
	cookie := cookieOf(w.signIn(password, nil))
	if rec := w.do("GET", "/settings", "", map[string]string{"Cookie": cookie}); rec.Code != http.StatusNotFound {
		t.Errorf("GET /settings: %d", rec.Code)
	}
	for _, target := range []string{"/settings/preset", "/settings/address", "/settings/address/remove"} {
		if rec := w.do("POST", target, "name=x&state=on", map[string]string{"Cookie": cookie}); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s: %d", target, rec.Code)
		}
	}
	if body := w.do("GET", "/", "", map[string]string{"Cookie": cookie}).Body.String(); strings.Contains(body, "/settings") {
		t.Error("the overview links to settings that do not exist")
	}
}

func TestChangingSettings(t *testing.T) {
	fake := &fakeSettings{
		presets:   []Preset{{Name: "block-ai-training", On: true}, {Name: "challenge-browsers"}},
		addresses: []Address{{Network: "192.0.2.0/24", Action: "deny", Note: `<script>alert(1)</script>`, Added: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}},
	}
	w := newWorld(t, func(o *Options) { o.Settings = fake })

	// Without a login: nothing, not even with a guessed form value.
	if rec := w.do("GET", "/settings", "", nil); rec.Code != http.StatusSeeOther {
		t.Errorf("GET /settings without a login: %d", rec.Code)
	}
	if rec := w.do("POST", "/settings/preset", "form=x&name=challenge-browsers&state=on", nil); rec.Code != http.StatusSeeOther || len(fake.calls) != 0 {
		t.Fatalf("POST without a login: %d, calls %v", rec.Code, fake.calls)
	}

	cookie := cookieOf(w.signIn(password, nil))
	auth := map[string]string{"Cookie": cookie}
	page := w.do("GET", "/settings", "", auth)
	body := page.Body.String()
	m := formValue.FindStringSubmatch(body)
	if page.Code != 200 || m == nil {
		t.Fatalf("GET /settings: %d", page.Code)
	}
	form := "form=" + url.QueryEscape(m[1])
	for _, want := range []string{"block-ai-training", "Blocks crawlers that collect pages", "192.0.2.0/24", "&lt;script&gt;alert(1)", "Switch off: block-ai-training", "Switch on: challenge-browsers"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(body, "<script>") {
		t.Error("a note reached the page as markup")
	}

	// A form that is not ours changes nothing.
	for name, attempt := range map[string]*httptest.ResponseRecorder{
		"no form value":     w.do("POST", "/settings/preset", "name=challenge-browsers&state=on", auth),
		"wrong form value":  w.do("POST", "/settings/preset", "form=AAAA&name=challenge-browsers&state=on", auth),
		"another site":      w.do("POST", "/settings/preset", form+"&name=challenge-browsers&state=on", map[string]string{"Cookie": cookie, "Origin": "https://evil.example"}),
		"cross-site fetch":  w.do("POST", "/settings/address", form+"&network=192.0.2.9&action=deny&lifetime=day", map[string]string{"Cookie": cookie, "Sec-Fetch-Site": "cross-site"}),
		"by a link":         w.do("GET", "/settings/preset?"+form+"&name=challenge-browsers&state=on", "", auth),
		"form value in URL": w.do("POST", "/settings/address/remove?"+form+"&network=192.0.2.0/24", "", auth),
	} {
		if attempt.Code == http.StatusSeeOther || len(fake.calls) != 0 {
			t.Errorf("%s: %d, calls %v", name, attempt.Code, fake.calls)
			fake.calls = nil
		}
	}

	// Our own form does.
	for _, body := range []string{
		"&name=challenge-browsers&state=on",
	} {
		if rec := w.do("POST", "/settings/preset", form+body, auth); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings?saved=1" {
			t.Errorf("preset: %d %q", rec.Code, rec.Body)
		}
	}
	w.do("POST", "/settings/address", form+"&network=198.51.100.7&action=allow&note=office&lifetime=week", auth)
	w.do("POST", "/settings/address/remove", form+"&network="+url.QueryEscape("192.0.2.0/24"), auth)
	want := []string{"preset challenge-browsers true", `add 198.51.100.7 allow "office" 168h0m0s`, "remove 192.0.2.0/24"}
	if strings.Join(fake.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %v", fake.calls)
	}

	// Values that are not among the choices never reach the settings.
	fake.calls = nil
	for _, bad := range []string{"&name=x&state=maybe", "&name=x"} {
		if rec := w.do("POST", "/settings/preset", form+bad, auth); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("preset %q: %d", bad, rec.Code)
		}
	}
	if rec := w.do("POST", "/settings/address", form+"&network=192.0.2.9&action=deny&lifetime=forever", auth); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown lifetime: %d", rec.Code)
	}
	if len(fake.calls) != 0 {
		t.Errorf("calls = %v", fake.calls)
	}

	// A refused change is explained in the reader's language.
	fake.fail = keyedError{"network_invalid"}
	rec := w.do("POST", "/settings/address", form+"&network=nonsense&action=deny&lifetime=day", map[string]string{"Cookie": cookie, "Accept-Language": "de"})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Das ist keine IP-Adresse") || strings.Contains(rec.Body.String(), "english text") {
		t.Errorf("refused: %d", rec.Code)
	}
	fake.fail = errors.New(`the rule "<b>" needs the trap`)
	rec = w.do("POST", "/settings/preset", form+"&name=block-trapped&state=on", auth)
	if !strings.Contains(rec.Body.String(), "That did not work: the rule &#34;&lt;b&gt;&#34; needs the trap") {
		t.Errorf("other error: %s", rec.Body.String()[:200])
	}

	if log := w.log.String(); strings.Contains(log, m[1]) || strings.Contains(log, "198.51.100.7") {
		t.Errorf("the log holds the form value or a listed address:\n%s", log)
	}
}

// Every preset that ships has its explanation in both languages.
func TestEveryPresetIsExplained(t *testing.T) {
	entries, err := fs.ReadDir(data.Files, "presets")
	if err != nil || len(entries) == 0 {
		t.Fatal(err)
	}
	w := newWorld(t, nil)
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".yaml")
		for _, lang := range languages {
			if w.a.texts[lang]["preset_"+name] == "" {
				t.Errorf("%s: no text for preset %s", lang, name)
			}
		}
	}
}

func (f *fakeSettings) Rules() string { return f.rules }
func (f *fakeSettings) SetRules(text string) error {
	f.calls = append(f.calls, "rules "+text)
	return f.fail
}
func (f *fakeSettings) Test(text string, probe Probe) (Verdict, error) {
	f.calls = append(f.calls, fmt.Sprintf("test %q %+v", text, probe))
	return Verdict{Action: "deny", Source: "rule:block-shop", Weight: 3}, f.fail
}
func (f *fakeSettings) Versions() []Version { return f.versions }
func (f *fakeSettings) Restore(number int) error {
	f.calls = append(f.calls, fmt.Sprintf("restore %d", number))
	return f.fail
}

type manyReasons []string

func (m manyReasons) Error() string   { return strings.Join(m, "; ") }
func (m manyReasons) Lines() []string { return m }

func TestRuleEditorTestBoxAndVersions(t *testing.T) {
	fake := &fakeSettings{rules: "rules: []\n", versions: []Version{
		{At: time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC), What: "preset_on:block-ai-training"},
		{At: time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC), What: "rules"},
	}}
	w := newWorld(t, func(o *Options) { o.Settings = fake })
	cookie := cookieOf(w.signIn(password, nil))
	auth := map[string]string{"Cookie": cookie}
	body := w.do("GET", "/settings", "", auth).Body.String()
	form := "form=" + url.QueryEscape(formValue.FindStringSubmatch(body)[1])
	for _, want := range []string{"rules: []", "Rule group block-ai-training switched on", "Own rules changed", "04.10.2026 10:00"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}

	// Not without our form, not from elsewhere, not without a login.
	draft := "&rules=" + url.QueryEscape("rules:\r\n  - name: x\r\n")
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"no form value": w.do("POST", "/settings/rules", "do=save"+draft, auth),
		"another site":  w.do("POST", "/settings/rules", form+"&do=save"+draft, map[string]string{"Cookie": cookie, "Origin": "https://evil.example"}),
		"no login":      w.do("POST", "/settings/rules", form+"&do=save"+draft, nil),
		"restore":       w.do("POST", "/settings/restore", "number=0", auth),
	} {
		if rec.Code == http.StatusOK || (rec.Code == http.StatusSeeOther && rec.Header().Get("Location") != "/login") || len(fake.calls) != 0 {
			t.Errorf("%s: %d, calls %v", name, rec.Code, fake.calls)
		}
	}

	// Trying does not save; the draft and the request stay on the page.
	probe := "&method=POST&address=" + url.QueryEscape("/shop?a=1") + "&client=192.0.2.9&user_agent=TestBot&headers=" + url.QueryEscape("Accept-Language: de\r\nX-A: \"><b>")
	rec := w.do("POST", "/settings/rules", form+"&do=test"+draft+probe, auth)
	page := rec.Body.String()
	if rec.Code != 200 || len(fake.calls) != 1 || !strings.HasPrefix(fake.calls[0], `test "rules:\n  - name: x\n" {Method:POST Address:/shop?a=1 UserAgent:TestBot Client:192.0.2.9`) {
		t.Fatalf("test: %d, calls %v", rec.Code, fake.calls)
	}
	for _, want := range []string{"Result: blocked", "decided by block-shop, score 3", "- name: x", `value="/shop?a=1"`, `value="192.0.2.9"`, "&gt;&lt;b&gt;", `<option selected>POST</option>`} {
		if !strings.Contains(page, want) {
			t.Errorf("after a test the page lacks %q", want)
		}
	}
	if strings.Contains(page, `"><b>`) {
		t.Error("a header of the probe reached the page as markup")
	}

	// Saving, and a rule set that is refused with several reasons.
	fake.calls = nil
	if rec := w.do("POST", "/settings/rules", form+"&do=save"+draft, auth); rec.Code != http.StatusSeeOther || fake.calls[0] != "rules rules:\n  - name: x\n" {
		t.Errorf("save: %d, calls %v", rec.Code, fake.calls)
	}
	fake.fail = manyReasons{"line 2, rules[0].action: an action is missing", `line 2: "<i>" is odd`}
	rec = w.do("POST", "/settings/rules", form+"&do=save"+draft, auth)
	page = rec.Body.String()
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(page, "<li>line 2, rules[0].action: an action is missing</li>") ||
		!strings.Contains(page, "&lt;i&gt;") || !strings.Contains(page, "- name: x") {
		t.Errorf("refused save: %d", rec.Code)
	}

	// Going back.
	fake.fail, fake.calls = nil, nil
	if rec := w.do("POST", "/settings/restore", form+"&number=1", auth); rec.Code != http.StatusSeeOther || fake.calls[0] != "restore 1" {
		t.Errorf("restore: %d, calls %v", rec.Code, fake.calls)
	}
	if rec := w.do("POST", "/settings/restore", form+"&number=abc", auth); rec.Code != http.StatusUnprocessableEntity || len(fake.calls) != 1 {
		t.Errorf("restore with a non-number: %d", rec.Code)
	}
	// A body far beyond what a rule text can be is not read.
	if rec := w.do("POST", "/settings/rules", form+"&do=save&rules="+strings.Repeat("a", 400<<10), auth); rec.Code == http.StatusSeeOther {
		t.Error("a 400 KiB rule text was accepted")
	}
}
