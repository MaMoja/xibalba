package changes

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseNetwork(t *testing.T) {
	good := map[string]string{
		"192.0.2.7": "192.0.2.7/32", " 192.0.2.77/24 ": "192.0.2.0/24", "::ffff:192.0.2.7": "192.0.2.7/32",
		"2001:db8::1": "2001:db8::1/128", "2001:db8:1:2::5/64": "2001:db8:1:2::/64", "10.1.0.0/16": "10.1.0.0/16",
	}
	for in, want := range good {
		got, err := ParseNetwork(in)
		if err != nil || got.String() != want {
			t.Errorf("ParseNetwork(%q) = %s, %v; want %s", in, got, err, want)
		}
	}
	bad := map[string]string{
		"": "network_invalid", "office": "network_invalid", "192.0.2.7/33": "network_invalid", "fe80::1%eth0": "network_invalid",
		"192.0.2.7 OR 1=1": "network_invalid", "<script>": "network_invalid", strings.Repeat("1", 500): "network_invalid",
		"0.0.0.0/0": "network_too_large", "10.0.0.0/8": "network_too_large", "::/0": "network_too_large", "2001:db8::/16": "network_too_large",
	}
	for in, key := range bad {
		_, err := ParseNetwork(in)
		var p *Problem
		if !errors.As(err, &p) || p.Key != key {
			t.Errorf("ParseNetwork(%q) = %v; want %s", in, err, key)
		}
		if err != nil && len(in) > 20 && strings.Contains(err.Error(), in) {
			t.Errorf("the error repeats a long input: %v", err)
		}
	}
}

type world struct {
	now     time.Time
	applied []State
	refuse  error
	path    string
}

func newStore(t *testing.T, initial State) (*Store, *world) {
	w := &world{now: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC), path: filepath.Join(t.TempDir(), "changes.json")}
	s := NewStore(Options{
		Path: w.path, Initial: initial, Presets: []string{"block-ai-training", "challenge-browsers"},
		Now: func() time.Time { return w.now },
		Apply: func(s State) error {
			if w.refuse != nil {
				return w.refuse
			}
			w.applied = append(w.applied, s)
			return nil
		},
	})
	return s, w
}

func key(err error) string {
	var p *Problem
	if errors.As(err, &p) {
		return p.Key
	}
	if err != nil {
		return "other: " + err.Error()
	}
	return ""
}

func TestChangesAreAppliedSavedAndRead(t *testing.T) {
	s, w := newStore(t, State{})
	if err := s.SetPreset("block-ai-training", true); err != nil {
		t.Fatal(err)
	}
	if err := s.Add("192.0.2.77/24", Deny, " scraper ", 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := s.Add("2001:db8::1", Allow, "", 0); err != nil {
		t.Fatal(err)
	}
	if len(w.applied) != 3 {
		t.Fatalf("applied %d times", len(w.applied))
	}
	loaded, err := Load(w.path)
	if err != nil || !loaded.Presets["block-ai-training"] || len(loaded.Addresses) != 2 {
		t.Fatalf("loaded = %+v, %v", loaded, err)
	}
	if e := loaded.Addresses[0]; e.Network != "192.0.2.0/24" || e.Note != "scraper" || !e.Expires.Equal(w.now.Add(24*time.Hour)) {
		t.Errorf("entry = %+v", e)
	}
	if info, _ := os.Stat(w.path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", info.Mode())
	}
	if err := loaded.Check([]string{"block-ai-training"}); err != nil {
		t.Errorf("what was saved does not pass the check: %v", err)
	}
	allow, deny := loaded.Active(w.now)
	if len(allow) != 1 || allow[0] != "2001:db8::1/128" || len(deny) != 1 || deny[0] != "192.0.2.0/24" {
		t.Errorf("active = %v, %v", allow, deny)
	}
	if _, deny = loaded.Active(w.now.Add(25 * time.Hour)); len(deny) != 0 {
		t.Errorf("an expired entry is still active: %v", deny)
	}

	if err := s.Remove("192.0.2.0/24"); err != nil {
		t.Fatal(err)
	}
	if got := s.State().Addresses; len(got) != 1 || got[0].Network != "2001:db8::1" {
		t.Errorf("after removal: %+v", got)
	}
}

func TestChangesThatAreRefused(t *testing.T) {
	s, w := newStore(t, State{})
	if err := s.Add("192.0.2.7", Deny, "", 0); err != nil {
		t.Fatal(err)
	}
	before := len(w.applied)
	for name, got := range map[string]error{
		"network_listed":   s.Add("192.0.2.7", Allow, "", 0),
		"network_invalid":  s.Add("nonsense", Deny, "", 0),
		"action_invalid":   s.Add("192.0.2.8", "weigh", "", 0),
		"note_invalid":     s.Add("192.0.2.8", Deny, strings.Repeat("x", MaxNote+1), 0),
		"note_invalid ":    s.Add("192.0.2.8", Deny, "line\nbreak", 0),
		"lifetime_invalid": s.Add("192.0.2.8", Deny, "", 5*MaxLifetime),
		"preset_unknown":   s.SetPreset("no-such-preset", true),
		"network_unknown":  s.Remove("198.51.100.1"),
	} {
		if key(got) != strings.TrimSpace(name) {
			t.Errorf("%s: %v", name, got)
		}
	}
	if len(w.applied) != before || len(s.State().Addresses) != 1 {
		t.Errorf("a refused change was applied or kept")
	}

	// What cannot be applied is not saved and not kept.
	w.refuse = errors.New("the rule set does not compile")
	if err := s.SetPreset("challenge-browsers", true); err == nil {
		t.Fatal("no error")
	}
	if loaded, _ := Load(w.path); len(loaded.Presets) != 0 || len(s.State().Presets) != 0 {
		t.Errorf("a change that could not be applied was saved: %+v", loaded)
	}

	// What cannot be saved is taken back.
	w.refuse = nil
	s.opts.Path = filepath.Join(t.TempDir(), "missing", "changes.json")
	if err := s.SetPreset("challenge-browsers", true); key(err) != "not_saved" {
		t.Fatalf("err = %v", err)
	}
	if last := w.applied[len(w.applied)-1]; len(last.Presets) != 0 {
		t.Errorf("the state in force after a failed save: %+v", last)
	}
}

func TestTheListIsBounded(t *testing.T) {
	var initial State
	for i := 0; i < MaxAddresses; i++ {
		initial.Addresses = append(initial.Addresses, Entry{Network: "10.0." + itoa(i/250) + "." + itoa(i%250+1), Action: Deny})
	}
	s, _ := newStore(t, initial)
	if err := s.Add("192.0.2.7", Deny, "", 0); key(err) != "too_many" {
		t.Errorf("err = %v", err)
	}
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + string(rune('0'+i%10))
}

func TestExpiredEntriesStopApplying(t *testing.T) {
	s, w := newStore(t, State{})
	if err := s.Add("192.0.2.7", Deny, "", time.Hour); err != nil {
		t.Fatal(err)
	}
	s.Tick()
	if len(w.applied) != 1 {
		t.Fatalf("applied again although nothing expired")
	}
	w.now = w.now.Add(61 * time.Minute)
	s.Tick()
	s.Tick()
	if len(w.applied) != 2 {
		t.Fatalf("applied %d times; want once more after the entry expired", len(w.applied))
	}
	// An expired entry makes room when the next one is added.
	if err := s.Add("192.0.2.8", Deny, "", 0); err != nil {
		t.Fatal(err)
	}
	if got := s.State().Addresses; len(got) != 1 || got[0].Network != "192.0.2.8" {
		t.Errorf("addresses = %+v", got)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	if s, err := Load(filepath.Join(dir, "none.json")); err != nil || !s.Empty() {
		t.Errorf("a missing file: %+v, %v", s, err)
	}
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte("{not json"), 0o600)
	if _, err := Load(bad); err == nil || strings.Contains(err.Error(), "not json") {
		t.Errorf("a damaged file: %v", err)
	}
	if _, err := Load(dir); err == nil {
		t.Error("a directory was read")
	}
	forged := State{Addresses: []Entry{{Network: "0.0.0.0/0", Action: Deny}}}
	if err := forged.Check(nil); key(err) != "network_too_large" {
		t.Errorf("a hand-made file that blocks everyone: %v", err)
	}
}
