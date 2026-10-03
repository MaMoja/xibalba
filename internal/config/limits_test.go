package config

import (
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestLimitsAreOffByDefault(t *testing.T) {
	cfg, err := Parse("xibalba.yaml", []byte(base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Limits.Enabled {
		t.Error("limits are on by default")
	}
}

func TestLimitSettings(t *testing.T) {
	cfg, err := Parse("xibalba.yaml", []byte(base+`limits:
  enabled: true
  count_by: network
  windows:
    - {requests: 100, per: 10s, action: challenge}
    - {requests: 5000, per: 24h, action: deny}
  exempt: ["192.0.2.0/24", "2001:db8::1"]
  max_clients: 2000
`))
	if err != nil {
		t.Fatal(err)
	}
	opts := cfg.Limits.Options()
	if !opts.ByNetwork || opts.MaxClients != 2000 || len(opts.Windows) != 2 ||
		opts.Windows[1].Requests != 5000 || opts.Windows[1].Per != 24*time.Hour || opts.Windows[1].Action != "deny" {
		t.Errorf("options = %+v", opts)
	}
	if len(opts.Exempt) != 2 || opts.Exempt[1] != netip.MustParsePrefix("2001:db8::1/128") {
		t.Errorf("exempt = %v", opts.Exempt)
	}
}

func TestLimitProblems(t *testing.T) {
	tests := []struct{ name, yaml, path, message string }{
		{"unknown way to count", "  count_by: person\n", "limits.count_by", "not a way to count"},
		{"no limit", "  windows: []\n", "limits.windows", "0 entries"},
		{"five limits", "  windows:\n" + strings.Repeat("    - {requests: 1, per: 1m, action: deny}\n", 5), "limits.windows", "5 entries"},
		{"zero requests", "  windows:\n    - {requests: 0, per: 1m, action: deny}\n", "limits.windows[0].requests", "out of range"},
		{"period too long", "  windows:\n    - {requests: 5, per: 48h, action: deny}\n", "limits.windows[0].per", "out of range"},
		{"period too short", "  windows:\n    - {requests: 5, per: 10ms, action: deny}\n", "limits.windows[0].per", "out of range"},
		{"same period twice", "  windows:\n    - {requests: 5, per: 1m, action: deny}\n    - {requests: 9, per: 60s, action: challenge}\n", "limits.windows[1].per", "already a limit"},
		{"allow is not a limit action", "  windows:\n    - {requests: 5, per: 1m, action: allow}\n", "limits.windows[0].action", "not an action a limit can take"},
		{"exempt entry is not an address", "  exempt: [\"office\"]\n", "limits.exempt[0]", "not an IP address"},
		{"everyone exempt", "  exempt: [\"0.0.0.0/0\"]\n", "limits.exempt[0]", "exempts every address"},
		{"table too small", "  max_clients: 10\n", "limits.max_clients", "out of range"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Checked even while switched off.
			_, err := Parse("xibalba.yaml", []byte(base+"limits:\n"+tt.yaml))
			if err == nil {
				t.Fatal("no error")
			}
			if text := err.Error(); !strings.Contains(text, tt.path) || !strings.Contains(text, tt.message) || !strings.Contains(text, "line ") {
				t.Errorf("error does not name %q, %q and a line:\n%s", tt.path, tt.message, text)
			}
		})
	}
}
