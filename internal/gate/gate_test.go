package gate

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
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
			{Name: "office", Match: rules.MatchSpec{IP: []string{"192.0.2.0/24"}}, Action: rules.Allow},
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
		{"score reaches the challenge threshold: passed on until the challenge exists", call{target: "/", headers: map[string]string{"Accept-Language": ""}}, 200, "threshold:10"},
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
