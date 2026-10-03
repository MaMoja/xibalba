package config

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// minimal is the smallest valid configuration: only the required setting.
// Tests append it after their own lines so the line numbers they check stay put.
const minimal = "upstream:\n  url: http://127.0.0.1:3000\n"

// withUpstream returns the defaults plus the upstream from minimal.
func withUpstream() Config {
	cfg := Default()
	cfg.Upstream.URL = "http://127.0.0.1:3000"
	return cfg
}

// settings returns cfg without what loading derives from the settings: the
// crawler definitions and the catalog built from them.
func settings(cfg Config) Config {
	cfg.Crawlers.Definitions = nil
	cfg.Rules.Catalog = nil
	cfg.Rules.TrapOn = false
	return cfg
}

func TestParseMinimalUsesDefaults(t *testing.T) {
	for _, input := range []string{minimal, "# a comment\n" + minimal, "log:\nops:\nserver:\n" + minimal} {
		cfg, err := Parse("test.yaml", []byte(input))
		if err != nil {
			t.Fatalf("Parse(%q) returned error: %v", input, err)
		}
		if !reflect.DeepEqual(settings(cfg), withUpstream()) {
			t.Errorf("Parse(%q) =\n%+v\nwant\n%+v", input, cfg, withUpstream())
		}
	}
}

func TestParseWithoutUpstreamIsAnError(t *testing.T) {
	for _, input := range []string{"", "   \n", "# only a comment\n", "log:\n  level: info\n"} {
		_, err := Parse("test.yaml", []byte(input))
		if err == nil || !strings.Contains(err.Error(), "upstream.url") {
			t.Errorf("Parse(%q) = %v, want an error about upstream.url", input, err)
		}
	}
}

func TestParseValid(t *testing.T) {
	input := `
log:
  level: debug
  format: text
server:
  listen: "0.0.0.0:8443"
  trusted_proxies:
    - 10.0.0.0/8
    - "::1"
  read_header_timeout: 3s
  idle_timeout: 2m
upstream:
  url: https://intern.example.org/app
  preserve_host: false
  dial_timeout: 2s
  response_header_timeout: 15s
ops:
  listen: "[::1]:9191"
shutdown_timeout: 30s
`
	cfg, err := Parse("test.yaml", []byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Config{
		Log: Log{Level: "debug", Format: "text"},
		Server: Server{
			Listen:            "0.0.0.0:8443",
			TrustedProxies:    []string{"10.0.0.0/8", "::1"},
			ReadHeaderTimeout: 3 * time.Second,
			IdleTimeout:       2 * time.Minute,
		},
		Upstream: Upstream{
			URL:                   "https://intern.example.org/app",
			PreserveHost:          false,
			DialTimeout:           2 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
		},
		Rules:           defaultRules(),
		Crawlers:        defaultCrawlers(),
		Limits:          defaultLimits(),
		Trap:            defaultTrap(),
		Challenge:       Default().Challenge,
		Pages:           Default().Pages,
		Ops:             Ops{Listen: "[::1]:9191"},
		ShutdownTimeout: 30 * time.Second,
	}
	if !reflect.DeepEqual(settings(cfg), want) {
		t.Errorf("got\n%+v\nwant\n%+v", cfg, want)
	}

	target := cfg.Upstream.Target()
	if target == nil || target.Host != "intern.example.org" || target.Path != "/app" {
		t.Errorf("Target() = %v", target)
	}
	wantPrefixes := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	if got := cfg.Server.TrustedPrefixes(); !reflect.DeepEqual(got, wantPrefixes) {
		t.Errorf("TrustedPrefixes() = %v, want %v", got, wantPrefixes)
	}
}

func TestParsePartialKeepsDefaults(t *testing.T) {
	cfg, err := Parse("test.yaml", []byte("log:\n  level: warn\n"+minimal))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := withUpstream()
	want.Log.Level = "warn"
	if !reflect.DeepEqual(settings(cfg), want) {
		t.Errorf("got\n%+v\nwant\n%+v", cfg, want)
	}
}

func TestParseProblems(t *testing.T) {
	tests := []struct {
		name  string
		input string   // minimal is appended unless the input sets upstream itself
		want  []string // substrings that must appear in the error
	}{
		{
			name:  "bad level with line",
			input: "log:\n  level: loud\n",
			want:  []string{"line 2, log.level", `"loud"`, "debug, info, warn, error"},
		},
		{
			name:  "bad format",
			input: "log:\n  format: xml\n",
			want:  []string{"line 2, log.format", "json, text"},
		},
		{
			name:  "ops listen without port",
			input: "ops:\n  listen: localhost\n",
			want:  []string{"line 2, ops.listen", "host:port"},
		},
		{
			name:  "ops listen with bad port",
			input: "ops:\n  listen: localhost:99999\n",
			want:  []string{"ops.listen", "0 to 65535"},
		},
		{
			name:  "server listen without port",
			input: "server:\n  listen: example.org\n",
			want:  []string{"line 2, server.listen", "host:port"},
		},
		{
			name:  "both listeners on one address",
			input: "server:\n  listen: 127.0.0.1:9000\nops:\n  listen: 127.0.0.1:9000\n",
			want:  []string{"line 4, ops.listen", "already used by server.listen"},
		},
		{
			name:  "bad trusted proxy points at the entry",
			input: "server:\n  trusted_proxies:\n    - 10.0.0.0/8\n    - not-an-ip\n",
			want:  []string{"line 4, server.trusted_proxies[1]", `"not-an-ip"`, "10.0.0.0/8"},
		},
		{
			name:  "zero server timeouts",
			input: "server:\n  read_header_timeout: 0s\n  idle_timeout: -1s\n",
			want:  []string{"server.read_header_timeout", "server.idle_timeout", "greater than zero"},
		},
		{
			name:  "upstream without scheme",
			input: "upstream:\n  url: localhost:3000\n",
			want:  []string{"line 2, upstream.url", "http://"},
		},
		{
			name:  "upstream with unsupported scheme",
			input: "upstream:\n  url: ftp://example.org\n",
			want:  []string{"upstream.url", `"http://" or "https://"`},
		},
		{
			name:  "upstream without host",
			input: "upstream:\n  url: \"http://\"\n",
			want:  []string{"upstream.url", "no host"},
		},
		{
			name:  "upstream with query",
			input: "upstream:\n  url: http://example.org/?a=1\n",
			want:  []string{"upstream.url", "query or fragment"},
		},
		{
			name:  "upstream with password",
			input: "upstream:\n  url: http://user:secret@example.org\n",
			want:  []string{"upstream.url", "user name or password"},
		},
		{
			name:  "zero upstream timeouts",
			input: "upstream:\n  url: http://example.org\n  dial_timeout: 0s\n  response_header_timeout: 0s\n",
			want:  []string{"upstream.dial_timeout", "upstream.response_header_timeout"},
		},
		{
			name:  "zero shutdown timeout",
			input: "shutdown_timeout: 0s\n",
			want:  []string{"line 1, shutdown_timeout", "greater than zero"},
		},
		{
			name:  "unknown setting",
			input: "logg:\n  level: info\n",
			want:  []string{"not valid", "logg", "docs/CONFIGURATION.md"},
		},
		{
			name:  "wrong type",
			input: "shutdown_timeout: [1, 2]\n",
			want:  []string{"not valid"},
		},
		{
			name:  "broken yaml",
			input: "log: [unclosed\n",
			want:  []string{"not valid"},
		},
		{
			name:  "all problems reported together",
			input: "log:\n  level: loud\n  format: xml\nshutdown_timeout: -1s\n",
			want:  []string{"3 problems", "log.level", "log.format", "shutdown_timeout"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := tt.input
			if !strings.Contains(input, "upstream:") {
				input += minimal
			}
			_, err := Parse("test.yaml", []byte(input))
			if err == nil {
				t.Fatal("expected an error, got none")
			}
			var cfgErr *Error
			if !errors.As(err, &cfgErr) {
				t.Fatalf("error is %T, want *config.Error", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "test.yaml") {
				t.Errorf("error does not name the file:\n%s", msg)
			}
			for _, w := range tt.want {
				if !strings.Contains(msg, w) {
					t.Errorf("error is missing %q:\n%s", w, msg)
				}
			}
		})
	}
}

// A password in the upstream URL must never be echoed into an error message
// or a log line.
func TestUpstreamPasswordIsNotEchoed(t *testing.T) {
	_, err := Parse("test.yaml", []byte("upstream:\n  url: http://user:hunter2@example.org\n"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("the password leaked into the error:\n%v", err)
	}
}

func TestSingleAddressBecomesHostPrefix(t *testing.T) {
	tests := map[string]string{
		"192.0.2.7":        "192.0.2.7/32",
		"2001:db8::1":      "2001:db8::1/128",
		"::ffff:192.0.2.7": "192.0.2.7/32", // IPv4-mapped is treated as IPv4
		"10.1.2.3/8":       "10.0.0.0/8",   // host bits are cleared
	}
	for entry, want := range tests {
		got, err := parsePrefix(entry)
		if err != nil || got.String() != want {
			t.Errorf("parsePrefix(%q) = %v, %v; want %s", entry, got, err, want)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()

	t.Run("missing file", func(t *testing.T) {
		_, err := Load(filepath.Join(dir, "nope.yaml"))
		if err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("got %v, want a 'does not exist' error", err)
		}
		if !strings.Contains(err.Error(), "xibalba.example.yaml") {
			t.Errorf("error should tell the user what to do: %v", err)
		}
	})

	t.Run("existing file", func(t *testing.T) {
		path := filepath.Join(dir, "ok.yaml")
		if err := os.WriteFile(path, []byte("log:\n  level: error\n"+minimal), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Log.Level != "error" {
			t.Errorf("level = %q, want error", cfg.Log.Level)
		}
	})
}

// The example file shipped in the repository must always be valid and must
// spell out the defaults, so it never drifts from the code. upstream.url is
// the one setting without a default; the example uses a typical local address.
func TestExampleFileMatchesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "xibalba.example.yaml"))
	if err != nil {
		t.Fatalf("xibalba.example.yaml is not valid: %v", err)
	}
	if !reflect.DeepEqual(settings(cfg), withUpstream()) {
		t.Errorf("xibalba.example.yaml =\n%+v\nwant the defaults\n%+v", cfg, withUpstream())
	}
}

func TestPagesSettings(t *testing.T) {
	cfg, err := loadLicensed(t, minimal+`
pages:
  operator: "Stadt Musterhausen"
  contact: "webmaster@musterhausen.example"
  default_language: en
  texts:
    de:
      operator: "Die Stadt Musterhausen"
      blocked_title: "Zugriff nicht möglich"
`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	opts := cfg.Pages.Options()
	if opts.Operator != "Stadt Musterhausen" || opts.Contact != "webmaster@musterhausen.example" ||
		opts.DefaultLanguage != "en" || opts.Texts["de"]["blocked_title"] != "Zugriff nicht möglich" || opts.HideAttribution {
		t.Errorf("pages settings not read: %+v", opts)
	}
}

func TestPagesProblemsPointAtTheLine(t *testing.T) {
	tests := []struct {
		name  string
		pages string // appended after minimal (2 lines), so "pages:" is line 3
		want  []string
	}{
		{"unknown language", "pages:\n  default_language: fr\n", []string{"line 4, pages.default_language", "de, en"}},
		{"unknown text name", "pages:\n  texts:\n    de:\n      blocked_heading: \"x\"\n", []string{"line 6, pages.texts.de.blocked_heading", "not a text Xibalba shows", "blocked_title"}},
		{"unknown placeholder", "pages:\n  texts:\n    en:\n      blocked_text: \"Ask {name}\"\n", []string{"line 6, pages.texts.en.blocked_text", "{name}", "{operator}"}},
		{"texts for an unknown language", "pages:\n  texts:\n    fr:\n      blocked_title: \"x\"\n", []string{"line 5, pages.texts.fr", "not a supported language"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadLicensed(t, minimal+tt.pages)
			if err == nil {
				t.Fatal("expected an error, got none")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error is missing %q:\n%v", w, err)
				}
			}
		})
	}
}

func TestChallengeSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "xibalba.yaml")
	content := minimal + `
challenge:
  difficulty: 20
  no_javascript: deny
  wait: 5s
  challenge_lifetime: 10m
  pass_lifetime: 24h
  bind_network: false
  key_file: xibalba.key
  cookie_name: site_pass
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Challenge{
		Difficulty: 20, NoJavaScript: "deny", Wait: 5 * time.Second,
		ChallengeLifetime: 10 * time.Minute, PassLifetime: 24 * time.Hour,
		BindNetwork: false, KeyFile: "xibalba.key", CookieName: "site_pass",
		KeyPath: filepath.Join(dir, "xibalba.key"), // relative to the configuration file
	}
	if cfg.Challenge != want {
		t.Errorf("got\n%+v\nwant\n%+v", cfg.Challenge, want)
	}
	if _, err := os.Stat(want.KeyPath); err == nil {
		t.Error("loading the configuration created the key file; that is the job of start-up, not of -check")
	}
}

func TestChallengeProblemsPointAtTheLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "short.key"), []byte("abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		challenge string // appended after minimal (2 lines), so "challenge:" is line 3
		want      []string
	}{
		{"difficulty too low", "challenge:\n  difficulty: 2\n", []string{"line 4, challenge.difficulty", "8 to 24"}},
		{"difficulty too high", "challenge:\n  difficulty: 40\n", []string{"line 4, challenge.difficulty", "8 to 24"}},
		{"unknown mode", "challenge:\n  no_javascript: captcha\n", []string{"line 4, challenge.no_javascript", "button", "deny"}},
		{"wait too short", "challenge:\n  wait: 0s\n", []string{"line 4, challenge.wait", `"1s" to "1m"`}},
		{"lifetime not longer than wait", "challenge:\n  wait: 40s\n  challenge_lifetime: 30s\n", []string{"line 5, challenge.challenge_lifetime", "nobody could answer in time"}},
		{"pass lifetime too long", "challenge:\n  pass_lifetime: 99999h\n", []string{"line 4, challenge.pass_lifetime", "one year"}},
		{"days are not a unit", "challenge:\n  pass_lifetime: 7d\n", []string{"not valid"}},
		{"cookie name with spaces", "challenge:\n  cookie_name: \"my pass\"\n", []string{"line 4, challenge.cookie_name", "xibalba-pass"}},
		{"cookie name with separators", "challenge:\n  cookie_name: \"a;b=c\"\n", []string{"line 4, challenge.cookie_name"}},
		{"key file in a missing directory", "challenge:\n  key_file: nowhere/xibalba.key\n", []string{"line 4, challenge.key_file", "does not exist", "created at the first start"}},
		{"key file is a directory", "challenge:\n  key_file: .\n", []string{"line 4, challenge.key_file", "is a directory"}},
		{"key file with a broken key", "challenge:\n  key_file: short.key\n", []string{"line 4, challenge.key_file", "does not hold a key", "delete the file"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(filepath.Join(dir, "xibalba.yaml"), []byte(minimal+tt.challenge))
			if err == nil {
				t.Fatal("expected an error, got none")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error is missing %q:\n%v", w, err)
				}
			}
		})
	}
}
