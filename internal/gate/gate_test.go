package gate

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MaMoja/xibalba/internal/clientip"
	"github.com/MaMoja/xibalba/internal/health"
	"github.com/MaMoja/xibalba/internal/rules"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func no() *bool { b := false; return &b }

func engine(t *testing.T) *rules.Engine {
	t.Helper()
	e, problems := rules.Compile(rules.Spec{
		DefaultAction: rules.Allow,
		Thresholds:    []rules.ThresholdSpec{{Weight: 10, Action: rules.Challenge}},
		Rules: []rules.RuleSpec{
			{Name: "office", Match: rules.MatchSpec{IP: []string{"192.0.2.0/24"}}, Action: rules.Allow, ExemptFromLimits: true},
			{Name: "block-bot", Match: rules.MatchSpec{UserAgent: &rules.StringSpec{Contains: "ExampleBot"}}, Action: rules.Deny},
			{Name: "block-admin", Match: rules.MatchSpec{Path: &rules.StringSpec{Prefix: "/admin"}}, Action: rules.Deny},
			{Name: "block-host", Match: rules.MatchSpec{Host: &rules.StringSpec{Equals: "internal.example.org"}}, Action: rules.Deny},
			{Name: "no-language", Match: rules.MatchSpec{Header: map[string]*rules.StringSpec{"Accept-Language": {Present: no()}}}, Action: rules.Weigh, Weight: 10},
		},
	})
	if len(problems) > 0 {
		t.Fatalf("test rule set does not compile: %+v", problems)
	}
	return e
}

// harness is a Gate in front of a fake website, with the client-identity
// stage before it as in the real program.
type harness struct {
	gate    *Gate
	handler http.Handler
	reached int // how many requests got through to the website
	now     time.Time
	mu      sync.Mutex
}

func newHarness(t *testing.T, change func(*Options)) *harness {
	t.Helper()
	h := &harness{now: time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)}
	opts := Options{
		Engine:   engine(t),
		FailOpen: true,
		Next: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			h.reached++
			_, _ = io.WriteString(w, "website")
		}),
		Blocked: func(w http.ResponseWriter, _ *http.Request, reference string) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, "blocked "+reference)
		},
		Unavailable: func(w http.ResponseWriter, _ *http.Request, status int) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, "unavailable")
		},
		Log: quiet(),
		Now: func() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.now },
	}
	if change != nil {
		change(&opts)
	}
	h.gate = New(opts)
	h.handler = clientip.Middleware(clientip.New([]netip.Prefix{netip.MustParsePrefix("10.0.0.1/32")}), h.gate)
	return h
}

func (h *harness) advance(d time.Duration) { h.mu.Lock(); h.now = h.now.Add(d); h.mu.Unlock() }

type call struct {
	method, target, remote string
	headers                map[string]string
}

func (h *harness) do(c call) *httptest.ResponseRecorder {
	if c.method == "" {
		c.method = http.MethodGet
	}
	if c.remote == "" {
		c.remote = "203.0.113.5:40000"
	}
	req := httptest.NewRequest(c.method, c.target, nil)
	req.RemoteAddr = c.remote
	req.Header.Set("Accept-Language", "de")
	for k, v := range c.headers {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func (h *harness) count(source string) uint64 {
	for _, s := range h.gate.Snapshot().Sources {
		if s.Source == source {
			return s.Count
		}
	}
	return 0
}

func TestEnforcement(t *testing.T) {
	tests := []struct {
		name       string
		call       call
		wantStatus int
		wantSource string
	}{
		{"ordinary visitor passes", call{target: "/"}, 200, "default"},
		{"denied by user agent", call{target: "/", headers: map[string]string{"User-Agent": "ExampleBot/2"}}, 403, "rule:block-bot"},
		{"denied by path", call{target: "/admin/users"}, 403, "rule:block-admin"},
		{"path spelled differently", call{target: "/x/..//ADMIN/"}, 403, "rule:block-admin"},
		{"path with encoded letters", call{target: "/%61dmin"}, 403, "rule:block-admin"},
		{"path with encoded dot segments", call{target: "/x/%2e%2e/admin"}, 403, "rule:block-admin"},
		{"denied by host, port and case ignored", call{target: "http://INTERNAL.example.org:8080/"}, 403, "rule:block-host"},
		{"allow rule comes first", call{target: "/admin", remote: "192.0.2.10:1"}, 200, "rule:office"},
		{"forged forwarding header does not make an office client", call{target: "/admin", headers: map[string]string{"X-Forwarded-For": "192.0.2.10"}}, 403, "rule:block-admin"},
		{"trusted proxy's header does", call{target: "/admin", remote: "10.0.0.1:1", headers: map[string]string{"X-Forwarded-For": "192.0.2.10"}}, 200, "rule:office"},
		{"score reaches the challenge threshold, no challenger configured: passed on", call{target: "/", headers: map[string]string{"Accept-Language": ""}}, 200, "threshold:10"},
		{"lower-case method", call{method: "get", target: "/admin"}, 403, "rule:block-admin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, nil)
			rec := h.do(tt.call)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := h.count(tt.wantSource); got != 1 {
				t.Errorf("count of %s = %d, want 1; snapshot: %+v", tt.wantSource, got, h.gate.Snapshot().Sources)
			}
			if passed := h.reached == 1; passed != (tt.wantStatus == 200) {
				t.Errorf("website reached = %v with status %d", passed, rec.Code)
			}
		})
	}
}

func TestBlockedPageGetsTheRuleReference(t *testing.T) {
	h := newHarness(t, nil)
	rec := h.do(call{target: "/admin"})
	var want string
	for _, s := range h.gate.Snapshot().Sources {
		if s.Source == "rule:block-admin" {
			want = s.Reference
		}
	}
	if want == "" || rec.Body.String() != "blocked "+want {
		t.Errorf("body = %q, want the reference %q of the rule that decided", rec.Body.String(), want)
	}
}

func TestDryRunCountsButBlocksNothing(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.DryRun = true })
	for _, target := range []string{"/", "/admin", "/admin/x"} {
		if rec := h.do(call{target: target}); rec.Code != 200 {
			t.Errorf("%s: status = %d, want 200 in dry-run", target, rec.Code)
		}
	}
	snap := h.gate.Snapshot()
	if !snap.DryRun || snap.Totals[rules.Deny] != 2 || snap.Totals[rules.Allow] != 1 || h.reached != 3 {
		t.Errorf("snapshot = %+v, reached = %d; want 2 would-be denials counted and all 3 requests passed", snap, h.reached)
	}
}

func TestSnapshotAndHandler(t *testing.T) {
	h := newHarness(t, nil)
	h.do(call{target: "/"})
	h.do(call{target: "/admin"})
	h.do(call{target: "/", headers: map[string]string{"Accept-Language": ""}})

	rec := httptest.NewRecorder()
	h.gate.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/decisions", nil))
	var snap Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, rec.Body.String())
	}
	if snap.Totals[rules.Allow] != 1 || snap.Totals[rules.Deny] != 1 || snap.Totals[rules.Challenge] != 1 {
		t.Errorf("totals = %+v", snap.Totals)
	}
	if len(snap.Sources) != 6 || snap.Sources[len(snap.Sources)-1].Source != "default" {
		t.Errorf("sources = %+v", snap.Sources)
	}
	if !snap.Since.Equal(time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("since = %v", snap.Since)
	}
	// Nothing about the visitors may appear in the counters.
	for _, leak := range []string{"203.0.113.5", "/admin", "Accept-Language"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("the counters contain %q", leak)
		}
	}
}

// broken is an engine that fails, to exercise the failure path.
type broken struct{ sources []rules.Source }

func (b broken) Evaluate(*rules.Request) rules.Decision { panic("engine exploded") }
func (b broken) Sources() []rules.Source                { return b.sources }

func TestEvaluationFailure(t *testing.T) {
	sources := []rules.Source{{ID: "default", Action: rules.Allow, Reference: "00000000"}}

	t.Run("fail open passes the request on", func(t *testing.T) {
		h := newHarness(t, func(o *Options) { o.Engine = broken{sources}; o.FailOpen = true })
		if rec := h.do(call{target: "/"}); rec.Code != 200 || h.reached != 1 {
			t.Errorf("status = %d, reached = %d", rec.Code, h.reached)
		}
	})

	t.Run("fail closed refuses with the unavailable page", func(t *testing.T) {
		h := newHarness(t, func(o *Options) { o.Engine = broken{sources}; o.FailOpen = false })
		rec := h.do(call{target: "/"})
		if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != "unavailable" || h.reached != 0 {
			t.Errorf("status = %d, body = %q, reached = %d", rec.Code, rec.Body.String(), h.reached)
		}
	})

	t.Run("health turns degraded and recovers", func(t *testing.T) {
		h := newHarness(t, func(o *Options) { o.Engine = broken{sources}; o.FailOpen = false })
		if got := h.gate.Health().State; got != health.OK {
			t.Fatalf("health before any failure = %q", got)
		}
		h.do(call{target: "/"})
		h.do(call{target: "/"})
		status := h.gate.Health()
		if status.State != health.Degraded {
			t.Fatalf("health after failures = %+v, want degraded", status)
		}
		for _, want := range []string{"2 requests", "refused", "engine exploded"} {
			if !strings.Contains(status.Detail, want) {
				t.Errorf("detail %q is missing %q", status.Detail, want)
			}
		}
		if h.gate.Snapshot().Failures != 2 {
			t.Errorf("failures = %d, want 2", h.gate.Snapshot().Failures)
		}
		h.advance(errorWindow + time.Second)
		if got := h.gate.Health().State; got != health.OK {
			t.Errorf("health after the window = %q, want ok", got)
		}
	})

	t.Run("a burst of failures writes one log line", func(t *testing.T) {
		var logs bytes.Buffer
		h := newHarness(t, func(o *Options) {
			o.Engine = broken{sources}
			o.Log = slog.New(slog.NewTextHandler(&logs, nil))
		})
		for i := 0; i < 50; i++ {
			h.do(call{target: "/"})
		}
		if n := strings.Count(logs.String(), "could not be evaluated"); n != 1 {
			t.Errorf("%d log lines for 50 failures, want 1", n)
		}
		if !strings.Contains(logs.String(), "component=rules") {
			t.Errorf("the log line does not name the component: %s", logs.String())
		}
	})
}

func TestDecisionsAreNotLoggedAboveDebug(t *testing.T) {
	var logs bytes.Buffer
	h := newHarness(t, func(o *Options) { o.Log = slog.New(slog.NewTextHandler(&logs, nil)) })
	h.do(call{target: "/admin"})
	if logs.Len() != 0 {
		t.Errorf("a denied request wrote to the log at the default level: %s", logs.String())
	}
}

func TestDebugLogHoldsNoPersonalData(t *testing.T) {
	var logs bytes.Buffer
	h := newHarness(t, func(o *Options) {
		o.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	h.do(call{target: "/admin/secret-page", headers: map[string]string{"User-Agent": "SomeBrowser/1"}})
	out := logs.String()
	if !strings.Contains(out, "source=rule:block-admin") {
		t.Errorf("the debug line does not name the deciding rule: %s", out)
	}
	for _, leak := range []string{"203.0.113.5", "secret-page", "SomeBrowser"} {
		if strings.Contains(out, leak) {
			t.Errorf("the debug log contains %q: %s", leak, out)
		}
	}
}

func TestConcurrentRequests(t *testing.T) {
	h := newHarness(t, func(o *Options) {
		o.Next = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	})
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				h.do(call{target: "/admin"})
			}
		}()
	}
	wg.Wait()
	if got := h.count("rule:block-admin"); got != 4000 {
		t.Errorf("count = %d, want 4000", got)
	}
}

// fakeChallenger passes requests that carry the header X-Pass.
type fakeChallenger struct{ served int }

func (f *fakeChallenger) Passed(r *http.Request) bool { return r.Header.Get("X-Pass") == "valid" }
func (f *fakeChallenger) Serve(w http.ResponseWriter, _ *http.Request) {
	f.served++
	w.WriteHeader(http.StatusForbidden)
	_, _ = io.WriteString(w, "challenge page")
}
func (f *fakeChallenger) Counts() (uint64, uint64) { return 7, 3 }

func TestChallengeEnforcement(t *testing.T) {
	noLanguage := map[string]string{"Accept-Language": ""} // weight 10 reaches the challenge threshold

	t.Run("without a pass the client gets the challenge page", func(t *testing.T) {
		ch := &fakeChallenger{}
		h := newHarness(t, func(o *Options) { o.Challenge = ch })
		rec := h.do(call{target: "/", headers: noLanguage})
		if rec.Code != http.StatusForbidden || rec.Body.String() != "challenge page" || h.reached != 0 || ch.served != 1 {
			t.Errorf("status %d, body %q, website reached %d, pages served %d", rec.Code, rec.Body.String(), h.reached, ch.served)
		}
	})

	t.Run("with a pass the request goes through", func(t *testing.T) {
		ch := &fakeChallenger{}
		h := newHarness(t, func(o *Options) { o.Challenge = ch })
		rec := h.do(call{target: "/", headers: map[string]string{"Accept-Language": "", "X-Pass": "valid"}})
		if rec.Code != 200 || h.reached != 1 || ch.served != 0 {
			t.Errorf("status %d, website reached %d, pages served %d", rec.Code, h.reached, ch.served)
		}
	})

	t.Run("a pass does not help against a deny rule", func(t *testing.T) {
		h := newHarness(t, func(o *Options) { o.Challenge = &fakeChallenger{} })
		if rec := h.do(call{target: "/admin", headers: map[string]string{"X-Pass": "valid"}}); rec.Code != http.StatusForbidden || h.reached != 0 {
			t.Errorf("status %d, website reached %d; a denied request must stay denied", rec.Code, h.reached)
		}
	})

	t.Run("allowed requests are never challenged", func(t *testing.T) {
		ch := &fakeChallenger{}
		h := newHarness(t, func(o *Options) { o.Challenge = ch })
		if rec := h.do(call{target: "/"}); rec.Code != 200 || ch.served != 0 {
			t.Errorf("status %d, pages served %d", rec.Code, ch.served)
		}
	})

	t.Run("dry run shows no challenge", func(t *testing.T) {
		ch := &fakeChallenger{}
		h := newHarness(t, func(o *Options) { o.Challenge = ch; o.DryRun = true })
		if rec := h.do(call{target: "/", headers: noLanguage}); rec.Code != 200 || ch.served != 0 {
			t.Errorf("status %d, pages served %d; dry run must enforce nothing", rec.Code, ch.served)
		}
	})

	t.Run("counters", func(t *testing.T) {
		h := newHarness(t, func(o *Options) { o.Challenge = &fakeChallenger{} })
		h.do(call{target: "/", headers: noLanguage})
		h.do(call{target: "/", headers: noLanguage})
		h.do(call{target: "/", headers: map[string]string{"Accept-Language": "", "X-Pass": "valid"}})
		got := h.gate.Snapshot().Challenge
		want := ChallengeCounts{Served: 2, Passed: 1, Solved: 7, Failed: 3}
		if got != want {
			t.Errorf("challenge counts = %+v, want %+v", got, want)
		}
	})
}

// The gate asks who a request claims to be and hands the answer to the rules.
func TestCrawlerIdentityReachesTheRules(t *testing.T) {
	yes := true
	e, problems := rules.Compile(rules.Spec{
		DefaultAction: rules.Deny,
		Crawlers:      &rules.Catalog{Classes: []string{"search-engine"}, Names: []string{"ExBot"}},
		Rules: []rules.RuleSpec{{Name: "genuine", Action: rules.Allow,
			Match: rules.MatchSpec{Crawler: &rules.CrawlerSpec{Name: []string{"ExBot"}, Verified: &yes}}}},
	})
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	var asked []string
	h := newHarness(t, func(o *Options) {
		o.Engine = e
		o.Identify = func(userAgent string, client netip.Addr) rules.Crawler {
			asked = append(asked, userAgent+" "+client.String())
			if userAgent != "ExBot" {
				return rules.Crawler{}
			}
			c := rules.Crawler{Name: "ExBot", Class: "search-engine", Status: rules.CrawlerImpostor}
			if client == netip.MustParseAddr("192.0.2.1") {
				c.Status = rules.CrawlerVerified
			}
			return c
		}
	})
	ua := map[string]string{"User-Agent": "ExBot"}
	if rec := h.do(call{target: "/", remote: "192.0.2.1:1", headers: ua}); rec.Code != http.StatusOK {
		t.Errorf("the genuine crawler got %d", rec.Code)
	}
	if rec := h.do(call{target: "/", remote: "203.0.113.5:1", headers: ua}); rec.Code != http.StatusForbidden {
		t.Errorf("the impostor got %d", rec.Code)
	}
	if len(asked) != 2 || asked[0] != "ExBot 192.0.2.1" {
		t.Errorf("asked = %v", asked)
	}

	// Without the hook a crawler condition never matches.
	h = newHarness(t, func(o *Options) { o.Engine = e })
	if rec := h.do(call{target: "/", remote: "192.0.2.1:1", headers: ua}); rec.Code != http.StatusForbidden {
		t.Errorf("without identification the crawler got %d", rec.Code)
	}
}

// limitHarness is a gate whose limiter says what the test tells it to.
func limitHarness(t *testing.T, over, deny *bool, counted *[]string, change func(*Options)) *harness {
	return newHarness(t, func(o *Options) {
		o.Limit = func(client netip.Addr) (bool, bool, time.Duration) {
			*counted = append(*counted, client.String())
			return *over, *deny, 42 * time.Second
		}
		o.Limited = func(w http.ResponseWriter, _ *http.Request, retryAfter time.Duration) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, "limited "+retryAfter.String())
		}
		o.Challenge = &fakeChallenger{}
		e, problems := rules.Compile(rules.Spec{DefaultAction: rules.Allow, Rules: []rules.RuleSpec{
			{Name: "office", Match: rules.MatchSpec{IP: []string{"192.0.2.0/24"}}, Action: rules.Allow, ExemptFromLimits: true},
			{Name: "public-files", Match: rules.MatchSpec{Path: &rules.StringSpec{Prefix: "/public/"}}, Action: rules.Allow},
			{Name: "block-admin", Match: rules.MatchSpec{Path: &rules.StringSpec{Prefix: "/admin"}}, Action: rules.Deny},
		}})
		if len(problems) > 0 {
			t.Fatal(problems)
		}
		o.Engine = e
		if change != nil {
			change(o)
		}
	})
}

func TestRequestLimits(t *testing.T) {
	var over, deny bool
	var counted []string
	h := limitHarness(t, &over, &deny, &counted, nil)

	// Under the limit nothing changes.
	if rec := h.do(call{target: "/"}); rec.Code != 200 {
		t.Fatalf("under the limit: %d", rec.Code)
	}
	if len(counted) != 1 || counted[0] != "203.0.113.5" {
		t.Fatalf("counted = %v", counted)
	}

	// Over a "challenge" limit the client must pass the check; with a pass it carries on.
	over = true
	if rec := h.do(call{target: "/"}); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "challenge") {
		t.Errorf("over a challenge limit: %d %q", rec.Code, rec.Body)
	}
	if rec := h.do(call{target: "/", headers: map[string]string{"X-Pass": "valid"}}); rec.Code != 200 {
		t.Errorf("over a challenge limit, with a pass: %d", rec.Code)
	}

	// Over a "deny" limit the client is refused, pass or not.
	deny = true
	rec := h.do(call{target: "/", headers: map[string]string{"X-Pass": "valid"}})
	if rec.Code != http.StatusTooManyRequests || rec.Body.String() != "limited 42s" {
		t.Errorf("over a deny limit: %d %q", rec.Code, rec.Body)
	}

	// A request a rule denies stays denied with the rule's page.
	if rec := h.do(call{target: "/admin"}); rec.Code != http.StatusForbidden || !strings.HasPrefix(rec.Body.String(), "blocked") {
		t.Errorf("denied by a rule and over the limit: %d %q", rec.Code, rec.Body)
	}

	// A request let through by a rule marked as exempt is neither counted nor limited.
	before := len(counted)
	if rec := h.do(call{target: "/", remote: "192.0.2.10:1"}); rec.Code != 200 {
		t.Errorf("allowed by an exempt rule: %d", rec.Code)
	}
	if len(counted) != before {
		t.Error("a request allowed by an exempt rule was counted")
	}
	// An ordinary allow rule does not exempt: anyone can ask for that address.
	if rec := h.do(call{target: "/public/a.css"}); rec.Code != http.StatusTooManyRequests {
		t.Errorf("allowed by an ordinary rule while over the limit: %d", rec.Code)
	}
	if len(counted) != before+1 {
		t.Error("a request allowed by an ordinary rule was not counted")
	}

	// A rule that lets through by path does not apply to an address written
	// in a roundabout way; the website might read it differently.
	over = false
	h2 := newHarness(t, func(o *Options) {
		e, problems := rules.Compile(rules.Spec{DefaultAction: rules.Deny, Rules: []rules.RuleSpec{
			{Name: "public-files", Match: rules.MatchSpec{Path: &rules.StringSpec{Prefix: "/public/"}}, Action: rules.Allow},
		}})
		if len(problems) > 0 {
			t.Fatal(problems)
		}
		o.Engine = e
	})
	for target, want := range map[string]int{
		"/public/a.css": 200, "/public/../public/a.css": 403, "/secret.php/..;/public/a.css": 403,
		"//public/a.css": 403, "/public/a%2Fb": 403, "/x/%2e%2e/public/a.css": 403,
		// Needlessly encoded: not what a browser sends, so not trusted either.
		"/public/%61.css": 403,
	} {
		req := httptest.NewRequest("GET", "http://example.org/", nil)
		u, err := url.Parse("http://example.org" + target) // as sent, nothing resolved
		if err != nil {
			t.Fatal(err)
		}
		req.URL = u
		req.RemoteAddr = "203.0.113.5:1"
		rec := httptest.NewRecorder()
		h2.handler.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("%s: %d, want %d", target, rec.Code, want)
		}
	}
}

func TestRequestLimitsInDryRun(t *testing.T) {
	over, deny := true, true
	var counted []string
	h := limitHarness(t, &over, &deny, &counted, func(o *Options) { o.DryRun = true })
	if rec := h.do(call{target: "/"}); rec.Code != 200 {
		t.Errorf("dry run: %d", rec.Code)
	}
	if len(counted) != 1 {
		t.Error("dry run did not count")
	}
}

func TestTrappedClientsReachTheRules(t *testing.T) {
	yes := true
	e, problems := rules.Compile(rules.Spec{DefaultAction: rules.Allow, Trap: true,
		Rules: []rules.RuleSpec{{Name: "caught", Action: rules.Deny, Match: rules.MatchSpec{Trapped: &yes}}}})
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	h := newHarness(t, func(o *Options) {
		o.Engine = e
		o.Trapped = func(client netip.Addr) bool { return client == netip.MustParseAddr("203.0.113.66") }
	})
	if rec := h.do(call{target: "/", remote: "203.0.113.66:1"}); rec.Code != http.StatusForbidden {
		t.Errorf("a trapped client got %d", rec.Code)
	}
	if rec := h.do(call{target: "/", remote: "203.0.113.67:1"}); rec.Code != 200 {
		t.Errorf("another client got %d", rec.Code)
	}
}

func TestCountryReachesTheRules(t *testing.T) {
	e, problems := rules.Compile(rules.Spec{DefaultAction: rules.Allow, Countries: true,
		Rules: []rules.RuleSpec{{Name: "abroad", Action: rules.Deny, Match: rules.MatchSpec{Country: []string{"FR"}}}}})
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	h := newHarness(t, func(o *Options) {
		o.Engine = e
		o.Country = func(client netip.Addr) ([2]byte, bool) {
			if client == netip.MustParseAddr("203.0.113.66") {
				return [2]byte{'F', 'R'}, true
			}
			return [2]byte{}, true
		}
	})
	if rec := h.do(call{target: "/", remote: "203.0.113.66:1"}); rec.Code != http.StatusForbidden {
		t.Errorf("a client from the denied country got %d", rec.Code)
	}
	if rec := h.do(call{target: "/", remote: "203.0.113.67:1"}); rec.Code != 200 {
		t.Errorf("a client of unknown country got %d", rec.Code)
	}

	// While no database is loaded, the rule is skipped: nobody is denied.
	loaded := false
	h = newHarness(t, func(o *Options) {
		o.Engine = e
		o.Country = func(netip.Addr) ([2]byte, bool) { return [2]byte{'F', 'R'}, loaded }
	})
	if rec := h.do(call{target: "/", remote: "203.0.113.66:1"}); rec.Code != 200 {
		t.Errorf("without a database: %d", rec.Code)
	}
	loaded = true
	if rec := h.do(call{target: "/", remote: "203.0.113.66:1"}); rec.Code != http.StatusForbidden {
		t.Errorf("with the database back: %d", rec.Code)
	}
}

// Pages are told by what the website answers, not by what the address looks like.
func TestPagesAreReportedFromTheAnswer(t *testing.T) {
	var pages []string
	over := false
	answers := map[string]struct {
		status int
		kind   string
	}{
		"/artikel/1":           {200, "text/html; charset=utf-8"},
		"/index.php/a/x.css":   {200, "TEXT/HTML"},
		"/bild.png":            {200, "image/png"},
		"/api/x":               {200, "application/json"},
		"/weg":                 {302, "text/html"},
		"/fehlt":               {404, "text/html"},
		"/ohne-typ":            {200, ""},
		"/public/../artikel/2": {200, "text/html"},
	}
	h := newHarness(t, func(o *Options) {
		o.Limit = func(netip.Addr) (bool, bool, time.Duration) { return over, true, time.Minute }
		o.Limited = func(w http.ResponseWriter, _ *http.Request, _ time.Duration) {
			w.WriteHeader(http.StatusTooManyRequests)
		}
		o.Page = func(client netip.Addr, path, query string) { pages = append(pages, client.String()+" "+path+"?"+query) }
		o.Next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			a := answers[r.URL.Path]
			if a.kind != "" {
				w.Header().Set("Content-Type", a.kind)
			}
			if a.status == 200 && r.URL.Path != "/ohne-typ" {
				_, _ = io.WriteString(w, "x") // an answer without an explicit status
				return
			}
			w.WriteHeader(a.status)
		})
	})
	for target := range answers {
		req := httptest.NewRequest("GET", "http://example.org/", nil)
		u, _ := url.Parse("http://example.org" + target + "?seite=2")
		req.URL = u
		req.RemoteAddr = "203.0.113.5:1"
		h.handler.ServeHTTP(httptest.NewRecorder(), req)
	}
	sort.Strings(pages)
	want := "203.0.113.5 /artikel/1?seite=2|203.0.113.5 /artikel/2?seite=2|203.0.113.5 /index.php/a/x.css?seite=2"
	if got := strings.Join(pages, "|"); got != want {
		t.Errorf("pages = %s\nwant    %s", got, want)
	}

	// Requests an exempt rule lets through, refused requests, and Xibalba's
	// own pages are not pages of the website.
	pages = nil
	h.do(call{target: "/artikel/1", remote: "192.0.2.10:1"}) // office: exempt
	h.do(call{target: "/admin"})                             // denied by a rule
	over = true
	h.do(call{target: "/artikel/1"}) // over the limit
	if len(pages) != 0 {
		t.Errorf("pages = %v", pages)
	}
}

// The watch must not get in the way of streaming and upgraded connections.
func TestPageWatchKeepsTheWriterUsable(t *testing.T) {
	rec := httptest.NewRecorder()
	found := 0
	w := &pageWatch{ResponseWriter: rec, found: func() { found++ }}
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusEarlyHints) // comes before the real answer
	if err := http.NewResponseController(w).Flush(); err != nil {
		t.Errorf("flush through the watch: %v", err)
	}
	_, _ = w.Write([]byte("a"))
	_, _ = w.Write([]byte("b"))
	w.WriteHeader(http.StatusOK)
	if found != 1 || rec.Body.String() != "ab" {
		t.Errorf("found %d times, body %q", found, rec.Body)
	}
}
