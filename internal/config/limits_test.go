package config

import (
	"github.com/MaMoja/xibalba/internal/admin"
	"net/netip"
	"os"
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
    - {requests: 100, per: 10s, action: challenge, deny_at: 400}
    - {requests: 5000, per: 24h, action: deny}
  exempt: ["192.0.2.0/24", "2001:db8::1"]
  max_clients: 2000
`))
	if err != nil {
		t.Fatal(err)
	}
	opts := cfg.Limits.Options()
	if !opts.ByNetwork || opts.MaxClients != 2000 || len(opts.Windows) != 2 ||
		opts.Windows[0].DenyAt != 400 || opts.Windows[1].Requests != 5000 || opts.Windows[1].Per != 24*time.Hour || opts.Windows[1].Action != "deny" {
		t.Errorf("options = %+v", opts)
	}
	if len(opts.Exempt) != 2 || opts.Exempt[1] != netip.MustParsePrefix("2001:db8::1/128") {
		t.Errorf("exempt = %v", opts.Exempt)
	}
}

func TestLimitOnPages(t *testing.T) {
	// The same period may carry one limit on requests and one on pages.
	cfg, err := Parse("xibalba.yaml", []byte(base+`limits:
  enabled: true
  windows:
    - {requests: 300, per: 10m, action: challenge}
    - {requests: 60, per: 10m, action: challenge, count: pages}
`))
	if err != nil {
		t.Fatal(err)
	}
	opts := cfg.Limits.Options()
	if len(opts.Windows) != 2 || opts.Windows[0].Pages || !opts.Windows[1].Pages || cfg.Limits.Windows[0].Count != "requests" {
		t.Errorf("options = %+v", opts)
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
		{"unknown thing to count", "  windows:\n    - {requests: 5, per: 1m, action: deny, count: visitors}\n", "limits.windows[0].count", "not something that can be counted"},
		{"too many pages", "  windows:\n    - {requests: 5000, per: 1m, action: deny, count: pages}\n", "limits.windows[0].requests", "out of range for a limit on pages"},
		{"same period and count twice", "  windows:\n    - {requests: 5, per: 1m, action: deny, count: pages}\n    - {requests: 9, per: 1m, action: challenge, count: pages}\n", "limits.windows[1].per", "already a limit on pages"},
		{"deny_at on a deny limit", "  windows:\n    - {requests: 5, per: 1m, action: deny, deny_at: 9}\n", "limits.windows[0].deny_at", "only has a meaning"},
		{"deny_at not above requests", "  windows:\n    - {requests: 5, per: 1m, action: challenge, deny_at: 5}\n", "limits.windows[0].deny_at", "out of range"},
		{"deny_at too many pages", "  windows:\n    - {requests: 5, per: 1m, action: challenge, count: pages, deny_at: 501}\n", "limits.windows[0].deny_at", "out of range"},
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

func TestTrapSettings(t *testing.T) {
	cfg, err := Parse("xibalba.yaml", []byte(base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Trap.Enabled || cfg.Trap.Maze {
		t.Error("the trap or the maze is on by default")
	}
	if _, err := Parse("xibalba.yaml", []byte(base+"trap:\n  enabled: true\n  maze: true\n  remember: 2h\nrules:\n  presets: [block-trapped]\n")); err != nil {
		t.Errorf("valid trap settings: %v", err)
	}
	tests := []struct{ name, yaml, path, message string }{
		{"remember too short", "trap:\n  remember: 1s\n", "trap.remember", "out of range"},
		{"table too small", "trap:\n  max_clients: 1\n", "trap.max_clients", "out of range"},
		{"maze without trap", "trap:\n  maze: true\n", "trap.maze", "the trap is off"},
		{"preset without trap", "rules:\n  presets: [block-trapped]\n", "preset block-trapped", "the trap is switched off"},
		{"rule without trap", "rules:\n  list:\n    - {name: r, match: {trapped: true}, action: deny}\n", "rules.list[0].match.trapped", "the trap is switched off"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse("xibalba.yaml", []byte(base+tt.yaml))
			if err == nil || !strings.Contains(err.Error(), tt.path) || !strings.Contains(err.Error(), tt.message) {
				t.Errorf("error = %v", err)
			}
		})
	}
}

func TestStatisticsSettings(t *testing.T) {
	cfg, err := Parse("xibalba.yaml", []byte(base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Statistics.Directory != "" || cfg.Statistics.Path != "" || cfg.Statistics.KeepDays != 400 ||
		cfg.Statistics.Networks.Enabled || cfg.Statistics.Networks.Top != 50 || cfg.Statistics.Networks.KeepDays != 30 {
		t.Errorf("defaults = %+v", cfg.Statistics)
	}
	path := writeFiles(t, map[string]string{"xibalba.yaml": base + "statistics:\n  directory: stats\n  keep_days: 30\n", "stats/.keep": "", "file": "x"})
	cfg, err = Load(path)
	if err != nil || !strings.HasSuffix(cfg.Statistics.Path, "/stats") {
		t.Errorf("cfg = %+v, err = %v", cfg.Statistics, err)
	}
	for yaml, want := range map[string]string{
		"statistics:\n  keep_days: 0\n":               "statistics.keep_days",
		"statistics:\n  networks: {enabled: true}\n":  "statistics.directory is empty",
		"statistics:\n  networks: {top: 0}\n":         "statistics.networks.top",
		"statistics:\n  networks: {keep_days: 500}\n": "statistics.networks.keep_days",
		"statistics:\n  keep_days: 99999\n":           "statistics.keep_days",
		"statistics:\n  directory: nowhere\n":         "does not exist",
		"statistics:\n  directory: file\n":            "is not a directory",
	} {
		_ = os.WriteFile(path, []byte(base+yaml), 0o600)
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", yaml, err)
		}
	}
}

func TestAdminSettings(t *testing.T) {
	cfg, err := Parse("xibalba.yaml", []byte(base))
	if err != nil {
		t.Fatal(err)
	}
	if a := cfg.Admin; a.Enabled || a.Listen != "127.0.0.1:9091" || a.PasswordFile != "admin.password" || a.SessionLifetime != 12*time.Hour {
		t.Errorf("defaults = %+v", a)
	}
	line, err := admin.HashPassword("a password for the tests")
	if err != nil {
		t.Fatal(err)
	}
	path := writeFiles(t, map[string]string{"xibalba.yaml": base + "admin:\n  enabled: true\n  password_file: secret/pw\n", "secret/pw": line + "\n"})
	if cfg, err = Load(path); err != nil || !cfg.Admin.Password.Matches("a password for the tests") {
		t.Fatalf("err = %v", err)
	}
	if got, err := PasswordFile(path); err != nil || !strings.HasSuffix(got, "/secret/pw") {
		t.Errorf("PasswordFile = %q, %v", got, err)
	}
	// The file is found even while the configuration is not valid yet.
	broken := writeFiles(t, map[string]string{"xibalba.yaml": base + "admin:\n  enabled: true\n"})
	if got, err := PasswordFile(broken); err != nil || !strings.HasSuffix(got, "/admin.password") {
		t.Errorf("PasswordFile = %q, %v", got, err)
	}
	bad := writeFiles(t, map[string]string{"xibalba.yaml": base + "admin:\n  enabled: true\n", "admin.password": "geheim123456\n"})
	for file, want := range map[string]string{broken: "does not exist", bad: "does not hold a stored password"} {
		_, err := Load(file)
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "-set-password") {
			t.Errorf("%s: %v", want, err)
		}
		if err != nil && strings.Contains(err.Error(), "geheim123456") {
			t.Errorf("the error repeats the file's content: %v", err)
		}
	}
	for yaml, want := range map[string]string{
		"admin:\n  listen: nowhere\n":       "admin.listen",
		"admin:\n  session_lifetime: 10s\n": "admin.session_lifetime",
		"admin:\n  password_file: \"\"\n":   "admin.password_file",
	} {
		if _, err := Parse("xibalba.yaml", []byte(base+yaml)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", yaml, err)
		}
	}
	// The same port as another listener only matters when switched on.
	same := "admin:\n  listen: \"127.0.0.1:9090\"\n"
	if _, err := Parse("xibalba.yaml", []byte(base+same)); err != nil {
		t.Errorf("switched off: %v", err)
	}
}
