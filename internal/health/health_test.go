package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fixed(s State, detail string) Check {
	return func() Status { return Status{State: s, Detail: detail} }
}

func TestReportWorstStateWins(t *testing.T) {
	tests := []struct {
		name   string
		states []State
		want   State
	}{
		{"no components", nil, OK},
		{"all ok", []State{OK, OK}, OK},
		{"one degraded", []State{OK, Degraded}, Degraded},
		{"one down", []State{Degraded, Down, OK}, Down},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry()
			for i, s := range tt.states {
				r.Register(string(rune('a'+i)), fixed(s, ""))
			}
			if got := r.Report().State; got != tt.want {
				t.Errorf("state = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPanickingCheckOnlyFailsItself(t *testing.T) {
	r := NewRegistry()
	r.Register("good", fixed(OK, ""))
	r.Register("bad", func() Status { panic("boom") })

	report := r.Report()
	if report.Components["good"].State != OK {
		t.Errorf("good component = %+v, want ok", report.Components["good"])
	}
	bad := report.Components["bad"]
	if bad.State != Down || !strings.Contains(bad.Detail, "boom") {
		t.Errorf("bad component = %+v, want down with the panic value", bad)
	}
}

func TestUnknownStateIsDown(t *testing.T) {
	r := NewRegistry()
	r.Register("odd", fixed(State("fine-ish"), ""))
	if got := r.Report().Components["odd"].State; got != Down {
		t.Errorf("state = %q, want down", got)
	}
}

func TestRegisterTwicePanics(t *testing.T) {
	r := NewRegistry()
	r.Register("a", fixed(OK, ""))
	defer func() {
		if recover() == nil {
			t.Error("registering the same name twice did not panic")
		}
	}()
	r.Register("a", fixed(OK, ""))
}

func TestHandler(t *testing.T) {
	tests := []struct {
		state State
		code  int
	}{
		{OK, http.StatusOK},
		{Degraded, http.StatusOK},
		{Down, http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			r := NewRegistry()
			r.Register("part", fixed(tt.state, "because"))

			rec := httptest.NewRecorder()
			r.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

			if rec.Code != tt.code {
				t.Errorf("status = %d, want %d", rec.Code, tt.code)
			}
			var got Report
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("body is not JSON: %v", err)
			}
			if got.State != tt.state || got.Components["part"].Detail != "because" {
				t.Errorf("report = %+v", got)
			}
		})
	}
}
