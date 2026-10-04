package metrics

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
)

func TestFormat(t *testing.T) {
	r := New()
	r.Add(func(w *Writer) {
		w.Counter("xibalba_decisions_total", "Decisions by action.", 12, "action", "allow", "source", "default")
		w.Counter("xibalba_decisions_total", "Decisions by action.", 3, "action", "deny", "source", `rule:block "bad"\bot`)
		w.Gauge("xibalba_clients", "Clients counted\nright now.", 1.5)
	})
	want := `# HELP xibalba_decisions_total Decisions by action.
# TYPE xibalba_decisions_total counter
xibalba_decisions_total{action="allow",source="default"} 12
xibalba_decisions_total{action="deny",source="rule:block \"bad\"\\bot"} 3
# HELP xibalba_clients Clients counted\nright now.
# TYPE xibalba_clients gauge
xibalba_clients 1.5
`
	if got := string(r.Render()); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// Whatever a label value holds, every line stays one well-formed line.
func TestHostileValuesCannotBreakTheFormat(t *testing.T) {
	line := regexp.MustCompile(`^(# (HELP|TYPE) [a-zA-Z_][a-zA-Z0-9_]* .*|[a-zA-Z_][a-zA-Z0-9_]*(\{([a-zA-Z_][a-zA-Z0-9_]*="([^"\\\n]|\\\\|\\"|\\n)*",?)+\})? [-+0-9.eE]+|[a-zA-Z_][a-zA-Z0-9_]* (NaN|[+-]Inf))$`)
	r := New()
	r.Add(func(w *Writer) {
		for _, v := range []string{"plain", "a\nfake_metric 1", `quote"} 99`, `back\slash\`, "", "ünïcode", "a\r\nb"} {
			w.Counter("m_total", "help", 1, "v", v)
		}
		w.Counter("bad name", "ignored", 1)
		w.Counter("9bad", "ignored", 1)
		w.Counter("ok_total", "h", 1, "bad label", "x", "good", "y")
		w.Counter("odd_total", "h", 1, "dangling")
	})
	out := string(r.Render())
	for _, l := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if !line.MatchString(strings.ReplaceAll(l, "\r", "")) {
			t.Errorf("malformed line: %q", l)
		}
	}
	if strings.Contains(out, "\nfake_metric") || strings.Contains(out, "bad name") || strings.Contains(out, "9bad") {
		t.Errorf("output:\n%s", out)
	}
	if !strings.Contains(out, `ok_total{good="y"} 1`) || !strings.Contains(out, "odd_total 1") {
		t.Errorf("output:\n%s", out)
	}
}

func TestAPanickingCollectorLosesOnlyItsOwnNumbers(t *testing.T) {
	r := New()
	r.Add(func(w *Writer) { w.Gauge("first", "h", 1) })
	r.Add(func(w *Writer) {
		w.Gauge("half", "h", 1)
		panic("broken")
	})
	r.Add(func(w *Writer) { w.Gauge("last", "h", 3) })
	out := string(r.Render())
	if !strings.Contains(out, "first 1") || !strings.Contains(out, "last 3") || strings.Contains(out, "half 1") {
		t.Errorf("output:\n%s", out)
	}
}

func TestHandlerAndConcurrentUse(t *testing.T) {
	r := New()
	r.Add(func(w *Writer) { w.Gauge("up", "Running.", 1) })
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") || !strings.Contains(rec.Body.String(), "up 1") {
		t.Errorf("content type %q, body %q", ct, rec.Body)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				r.Render()
				r.Add(func(*Writer) {})
			}
		}()
	}
	wg.Wait()
}
