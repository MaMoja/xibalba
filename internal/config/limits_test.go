package config

import (
	"github.com/MaMoja/xibalba/internal/admin"
	"github.com/MaMoja/xibalba/internal/changes"
	"github.com/MaMoja/xibalba/internal/rules"
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
		"admin:\n  listen: nowhere\n":                    "admin.listen",
		"admin:\n  hostnames: [\"https://x.example\"]\n": "admin.hostnames[0]",
		"admin:\n  enabled: true\n  listen: \":9090\"\n": "already used by ops.listen",
		"admin:\n  session_lifetime: 10s\n":              "admin.session_lifetime",
		"admin:\n  password_file: \"\"\n":                "admin.password_file",
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

func TestChangesFromTheWebInterface(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	changed := `{"presets": {"block-ai-training": true, "allow-feeds": false},
 "addresses": [{"network": "192.0.2.0/24", "action": "deny", "added": "2026-10-01T00:00:00Z"},
               {"network": "198.51.100.7", "action": "allow", "added": "2026-10-01T00:00:00Z", "expires": "2026-10-02T00:00:00Z"}]}`
	path := writeFiles(t, map[string]string{
		"xibalba.yaml":       base + "rules:\n  presets: [allow-feeds, challenge-browsers]\n",
		"admin.changes.json": changed,
	})
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// In force although the web interface is off: the file decides.
	if got := cfg.Rules.EffectivePresets(cfg.Admin.Changes); strings.Join(got, ",") != "block-ai-training,challenge-browsers" {
		t.Errorf("presets = %v", got)
	}
	spec, err := cfg.RuleSpec(cfg.Admin.Changes, now)
	if err != nil {
		t.Fatal(err)
	}
	// The expired entry is left out, the list comes first.
	if spec.Rules[0].Name != ListDenyRule || len(spec.Rules[0].Match.IP) != 1 || spec.Rules[1].Name == ListAllowRule {
		t.Errorf("first rules = %+v", spec.Rules[:2])
	}
	if _, problems := rules.Compile(spec); len(problems) != 0 {
		t.Errorf("problems = %+v", problems)
	}

	for name, tt := range map[string]struct{ file, want string }{
		"damaged":           {"{", "is not valid JSON"},
		"blocks everyone":   {`{"addresses": [{"network": "0.0.0.0/0", "action": "deny"}]}`, "not valid"},
		"unknown preset":    {`{"presets": {"nope": true}}`, "is not a preset"},
		"needs what is off": {`{"presets": {"block-trapped": true}}`, "the rule set does not work"},
		"unknown action":    {`{"addresses": [{"network": "192.0.2.1", "action": "weigh"}]}`, "not valid"},
	} {
		path := writeFiles(t, map[string]string{"xibalba.yaml": base, "admin.changes.json": tt.file})
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "admin.changes_file") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := Parse("xibalba.yaml", []byte(base+"admin:\n  allow_changes: true\n")); err == nil || !strings.Contains(err.Error(), "admin.allow_changes") {
		t.Errorf("changes allowed without the web interface: %v", err)
	}
}

func TestWhereASwitchedOnPresetGoes(t *testing.T) {
	r := Rules{Presets: []string{"challenge-browsers", "allow-feeds"}} // the owner's order is kept
	on := func(names ...string) changes.State {
		s := changes.State{Presets: map[string]bool{}}
		for _, n := range names {
			s.Presets[n] = true
		}
		return s
	}
	for want, state := range map[string]changes.State{
		"challenge-browsers,allow-feeds":                                         {},
		"block-ai-training,challenge-browsers,allow-feeds":                       on("block-ai-training"),
		"keep-internet-working,block-ai-training,challenge-browsers,allow-feeds": on("block-ai-training", "keep-internet-working", "allow-feeds"),
	} {
		if got := strings.Join(r.EffectivePresets(state), ","); got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	}
	if len(PresetsInOrder()) != len(PresetNames()) {
		t.Errorf("order lists %d presets, there are %d", len(PresetsInOrder()), len(PresetNames()))
	}
}

// A blocked address inside a network that is let through stays blocked, and
// the two rule names of the list cannot be taken by the owner's own rules.
func TestListedBlockBeatsListedAllow(t *testing.T) {
	path := writeFiles(t, map[string]string{
		"xibalba.yaml":       base,
		"admin.changes.json": `{"addresses": [{"network": "198.51.100.0/24", "action": "allow"}, {"network": "198.51.100.7", "action": "deny"}]}`,
	})
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := cfg.RuleSpec(cfg.Admin.Changes, time.Now())
	engine, problems := rules.Compile(spec)
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	for addr, want := range map[string]rules.Action{"198.51.100.7": rules.Deny, "198.51.100.8": rules.Allow} {
		if got := engine.Evaluate(&rules.Request{Method: "GET", Path: "/", Client: netip.MustParseAddr(addr)}); got.Action != want {
			t.Errorf("%s: %s, want %s", addr, got.Action, want)
		}
	}

	taken := base + "rules:\n  list:\n    - name: web-interface.allow\n      match: {path: {prefix: \"/x\"}}\n      action: allow\n"
	if _, err := Parse("xibalba.yaml", []byte(taken)); err == nil || !strings.Contains(err.Error(), "kept for the address list") {
		t.Errorf("a reserved rule name: %v", err)
	}

	// Entries past their time do not count and do not keep Xibalba from starting.
	old := `{"addresses": [{"network": "0.0.0.0", "action": "deny", "expires": "2020-01-01T00:00:00Z"}]}`
	cfg, err = Load(writeFiles(t, map[string]string{"xibalba.yaml": base, "admin.changes.json": old}))
	if err != nil || len(cfg.Admin.Changes.Addresses) != 0 {
		t.Errorf("an expired entry: %+v, %v", cfg.Admin.Changes, err)
	}
}

func TestRulesWrittenInTheWebInterface(t *testing.T) {
	cfg, err := Parse("xibalba.yaml", []byte(base+"rules:\n  presets: [challenge-browsers]\n  list:\n    - name: from-file\n      match: {path: {prefix: \"/\"}}\n      action: deny\n"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	own := "# my rules\nrules:\n  - name: let-status\n    match:\n      path: {equals: \"/status\"}\n    action: allow\n"
	state := changes.State{Rules: own, Addresses: []changes.Entry{{Network: "192.0.2.7", Action: changes.Deny}}}
	engine, problems := cfg.Compile(state, now)
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	// Order: the address list, then own rules, then the configuration.
	for _, tt := range []struct{ addr, path, want string }{
		{"192.0.2.7", "/status", "rule:web-interface.deny"},
		{"192.0.2.8", "/status", "rule:let-status"},
		{"192.0.2.8", "/other", "rule:from-file"},
	} {
		d := engine.Evaluate(&rules.Request{Method: "GET", Path: tt.path, Client: netip.MustParseAddr(tt.addr)})
		if got := engine.Sources()[d.Source].ID; got != tt.want {
			t.Errorf("%s %s: decided by %s, want %s", tt.addr, tt.path, got, tt.want)
		}
	}

	for name, tt := range map[string]struct{ rules, want string }{
		"problem with its line":  {"rules:\n  - name: a\n    match: {path: {prefix: \"/x\"}}\n    action: fly\n", "line 4, rules[0].action"},
		"not YAML":               {"rules:\n  - name: [\n", "the rules are not valid"},
		"unknown key":            {"regeln: []\n", "the rules are not valid"},
		"name already used":      {"rules:\n  - name: from-file\n    match: {path: {prefix: \"/x\"}}\n    action: allow\n", "already used"},
		"reserved name":          {"rules:\n  - name: web-interface.deny\n    match: {path: {prefix: \"/x\"}}\n    action: allow\n", "kept for Xibalba's own rules"},
		"preset's name":          {"rules:\n  - name: preset.mine\n    match: {path: {prefix: \"/x\"}}\n    action: allow\n", "kept for Xibalba's own rules"},
		"exempt without address": {"rules:\n  - name: a\n    match: {path: {prefix: \"/x\"}}\n    action: allow\n    exempt_from_limits: true\n", "line"},
	} {
		engine, problems := cfg.Compile(changes.State{Rules: tt.rules}, now)
		if engine != nil || len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), tt.want) {
			t.Errorf("%s: %v", name, problems)
		}
	}
	// Empty text and comments only mean: no own rules.
	for _, text := range []string{"", "\n\n", "# nothing yet\n"} {
		if _, problems := cfg.Compile(changes.State{Rules: text}, now); len(problems) != 0 {
			t.Errorf("%q: %v", text, problems)
		}
	}
}

// A small text must not become a large rule set, and no text crashes the reader.
func TestRuleTextThatIsNotPlain(t *testing.T) {
	cfg, err := Parse("xibalba.yaml", []byte(base))
	if err != nil {
		t.Fatal(err)
	}
	bomb := "rules:\n  - name: a\n    action: deny\n    match:\n      any: [&l1 {any: [&l2 {any: [&l3 {path: {prefix: \"/x\"}}, *l3, *l3, *l3]}, *l2, *l2, *l2]}, *l1, *l1, *l1]\n"
	for name, tt := range map[string]struct{ text, want string }{
		"aliases":         {bomb, "anchors and aliases"},
		"merge key":       {"base: &b {action: deny}\nrules:\n  - <<: *b\n    name: a\n", "anchors and aliases"},
		"tag":             {"rules: !!binary aGVsbG8=\n", "tags"},
		"two documents":   {"rules: []\n---\nrules: []\n", "more than one document"},
		"deep brackets":   {"rules: " + strings.Repeat("[", 5000) + "\n", "nested deeper"},
		"deep indent":     {"rules:\n" + strings.Repeat(" ", 200) + "- name: a\n", "nested deeper"},
		"not YAML at all": {"rules: {\"a\": \n\t\x7f", "not valid"},
	} {
		start := time.Now()
		engine, problems := cfg.Compile(changes.State{Rules: tt.text}, start)
		if engine != nil || len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), tt.want) {
			t.Errorf("%s: %v", name, problems)
		}
		if took := time.Since(start); took > 2*time.Second {
			t.Errorf("%s took %s", name, took)
		}
	}
}
