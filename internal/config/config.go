// Package config loads and validates the Xibalba configuration file.
//
// The package has one job: turn a YAML file into a Config value that the rest
// of the program can trust. Every value is checked here, once, at start-up, so
// no other package needs to defend against a malformed setting.
//
// Errors are written for the person editing the file: they name the file, the
// line, the setting, what is wrong and how to fix it. All problems are reported
// together instead of one per run.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"

	"github.com/MaMoja/xibalba/internal/challenge"
	"github.com/MaMoja/xibalba/internal/pages"
	"github.com/MaMoja/xibalba/internal/token"
)

// Config is the complete, validated configuration.
//
// Every field is documented in docs/CONFIGURATION.md. When a field is added
// here, add it there and to xibalba.example.yaml in the same change.
type Config struct {
	// Log controls what Xibalba writes to its log.
	Log Log `yaml:"log"`
	// Server is the public listener that visitors connect to.
	Server Server `yaml:"server"`
	// Upstream is the website Xibalba protects.
	Upstream Upstream `yaml:"upstream"`
	// Rules decide what happens to each request.
	Rules Rules `yaml:"rules"`
	// Crawlers says how crawlers are recognised and verified.
	Crawlers Crawlers `yaml:"crawlers"`
	// Admin is the optional web interface.
	Admin Admin `yaml:"admin"`
	// Statistics keeps counters by the hour on disk.
	Statistics Statistics `yaml:"statistics"`
	// Countries is the database that says which country an address is in.
	Countries Countries `yaml:"countries"`
	// Trap is the hidden link that catches crawlers.
	Trap Trap `yaml:"trap"`
	// Verdict answers a web server that asks about each request.
	Verdict Verdict `yaml:"verdict"`
	// Previews puts a page's link-preview tags on the challenge page.
	Previews Previews `yaml:"previews"`
	// Limits are the request limits per client.
	Limits Limits `yaml:"limits"`
	// Challenge is the check a client has to pass when a rule says "challenge".
	Challenge Challenge `yaml:"challenge"`
	// Pages adapts the pages Xibalba shows to visitors.
	Pages Pages `yaml:"pages"`
	// License is the sponsor license, which unlocks some of the Pages settings.
	License License `yaml:"license"`
	// Ops is the internal listener for health checks and, later, metrics.
	Ops Ops `yaml:"ops"`
	// ShutdownTimeout is how long running requests get to finish on shutdown.
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

// Log holds the logging settings.
type Log struct {
	// Level is the lowest severity that is written: debug, info, warn or error.
	Level string `yaml:"level"`
	// Format is "json" (one object per line) or "text" (for reading by eye).
	Format string `yaml:"format"`
}

// Server holds the settings of the public listener.
type Server struct {
	// Listen is the host:port visitors (or the web server in front) connect to.
	Listen string `yaml:"listen"`
	// TrustedProxies lists the addresses and networks of reverse proxies and
	// load balancers in front of Xibalba. Only their forwarding headers are
	// believed. Empty means Xibalba is reached directly and trusts no header.
	TrustedProxies []string `yaml:"trusted_proxies"`
	// ReadHeaderTimeout is how long a client may take to send its request headers.
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	// IdleTimeout is how long an unused keep-alive connection stays open.
	IdleTimeout time.Duration `yaml:"idle_timeout"`
}

// TrustedPrefixes returns TrustedProxies as network prefixes. The values were
// validated at load time, so entries that fail to parse cannot occur and are skipped.
func (s Server) TrustedPrefixes() []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(s.TrustedProxies))
	for _, entry := range s.TrustedProxies {
		if p, err := parsePrefix(entry); err == nil {
			prefixes = append(prefixes, p)
		}
	}
	return prefixes
}

// Upstream holds the settings of the protected website.
type Upstream struct {
	// URL is where the website is reached, for example "http://127.0.0.1:3000".
	URL string `yaml:"url"`
	// PreserveHost sends the visitor's Host header to the website instead of
	// the host from URL. Most websites need this to build correct links.
	PreserveHost bool `yaml:"preserve_host"`
	// DialTimeout is how long connecting to the website may take.
	DialTimeout time.Duration `yaml:"dial_timeout"`
	// ResponseHeaderTimeout is how long the website may take to start answering.
	ResponseHeaderTimeout time.Duration `yaml:"response_header_timeout"`
}

// Target returns URL parsed. The value was validated at load time; if it is
// somehow invalid the result is nil.
func (u Upstream) Target() *url.URL {
	target, err := parseUpstream(u.URL)
	if err != nil {
		return nil
	}
	return target
}

// NoJavaScriptModes are the allowed values of challenge.no_javascript.
var NoJavaScriptModes = []string{"button", "deny"}

// Challenge holds the settings of the security check.
type Challenge struct {
	// Method is the kind of check: pow (a calculation in the browser),
	// script (the browser runs a small script and waits), wait (wait, then
	// press a button; no JavaScript) or refresh (wait, then be sent on; no
	// JavaScript).
	Method string `yaml:"method"`
	// Checks are extra checks on top of pow or script: css, headless.
	Checks []string `yaml:"checks"`
	// Difficulty is the proof of work in leading zero bits. Each extra bit
	// doubles the work a client has to do.
	Difficulty int `yaml:"difficulty"`
	// NoJavaScript says what visitors without JavaScript get: "button" lets
	// them wait and press a button, "deny" tells them JavaScript is needed.
	NoJavaScript string `yaml:"no_javascript"`
	// Wait is how long a visitor without JavaScript must wait before the
	// button counts.
	Wait time.Duration `yaml:"wait"`
	// ChallengeLifetime is how long a client has to answer.
	ChallengeLifetime time.Duration `yaml:"challenge_lifetime"`
	// PassLifetime is how long a client is not asked again after passing.
	PassLifetime time.Duration `yaml:"pass_lifetime"`
	// BindNetwork ties a pass to the client's network as well as its browser.
	BindNetwork bool `yaml:"bind_network"`
	// KeyFile is where the signing key is kept. Empty means a new key at
	// every start, which ends all passes on restart.
	KeyFile string `yaml:"key_file"`
	// CookieName is the name of the pass cookie.
	CookieName string `yaml:"cookie_name"`

	// KeyPath is KeyFile resolved against the directory of the configuration
	// file. It is filled when the configuration is loaded and is not a setting.
	KeyPath string `yaml:"-"`
}

// check validates the challenge settings and resolves the key file. dir is
// the directory relative paths are resolved against.
func (c *Challenge) check(dir string, add func(path, message, hint string)) {
	if c.Difficulty < challenge.MinDifficulty || c.Difficulty > challenge.MaxDifficulty {
		add("challenge.difficulty", fmt.Sprintf("%d is out of range", c.Difficulty),
			fmt.Sprintf("use a value from %d to %d; 18 suits most sites", challenge.MinDifficulty, challenge.MaxDifficulty))
	}
	if !contains(challenge.Methods, c.Method) {
		add("challenge.method", fmt.Sprintf("%q is not a kind of security check", c.Method),
			"use one of: "+strings.Join(challenge.Methods, ", "))
	}
	for i, check := range c.Checks {
		if !contains(challenge.Checks, check) {
			add(fmt.Sprintf("challenge.checks[%d]", i), fmt.Sprintf("%q is not an extra check", check), "use: "+strings.Join(challenge.Checks, ", "))
		}
	}
	if len(c.Checks) > 0 && (c.Method == challenge.MethodWait || c.Method == challenge.MethodRefresh) {
		add("challenge.checks", fmt.Sprintf("extra checks run in JavaScript, and the method %s does without it", c.Method),
			"use method pow or script, or remove the checks")
	}
	if !contains(NoJavaScriptModes, c.NoJavaScript) {
		add("challenge.no_javascript", fmt.Sprintf("%q is not a mode", c.NoJavaScript),
			"use button (visitors without JavaScript wait and press a button) or deny (they are told JavaScript is needed)")
	}
	if c.Wait < time.Second || c.Wait > time.Minute {
		add("challenge.wait", fmt.Sprintf("%s is out of range", c.Wait), `use a duration from "1s" to "1m"`)
	}
	if c.ChallengeLifetime < 30*time.Second || c.ChallengeLifetime > time.Hour {
		add("challenge.challenge_lifetime", fmt.Sprintf("%s is out of range", c.ChallengeLifetime), `use a duration from "30s" to "1h"`)
	} else if c.ChallengeLifetime <= c.Wait {
		add("challenge.challenge_lifetime", fmt.Sprintf("%s is not longer than challenge.wait (%s), so nobody could answer in time", c.ChallengeLifetime, c.Wait),
			"make challenge_lifetime longer than wait")
	}
	if c.PassLifetime < time.Minute || c.PassLifetime > 8760*time.Hour {
		add("challenge.pass_lifetime", fmt.Sprintf("%s is out of range", c.PassLifetime),
			`use a duration from "1m" to "8760h" (one year); a week is "168h"`)
	}
	if !isCookieName(c.CookieName) {
		add("challenge.cookie_name", fmt.Sprintf("%q is not usable as a cookie name", c.CookieName),
			`use letters, digits, hyphen and underscore, at most 64 characters, for example "xibalba-pass"`)
	}

	c.KeyPath = ""
	if strings.TrimSpace(c.KeyFile) == "" {
		return
	}
	path := c.KeyFile
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	path = filepath.Clean(path)
	c.KeyPath = path

	info, err := os.Stat(path)
	switch {
	case err == nil && info.IsDir():
		add("challenge.key_file", fmt.Sprintf("%q is a directory", c.KeyFile), "give the path of a file, for example /var/lib/xibalba/xibalba.key")
	case err == nil:
		data, err := os.ReadFile(path)
		if err != nil {
			add("challenge.key_file", fmt.Sprintf("%q cannot be read", c.KeyFile), "make the file readable for the user Xibalba runs as")
		} else if _, err := token.ParseKey(string(data)); err != nil {
			add("challenge.key_file", fmt.Sprintf("%q does not hold a key: %v", c.KeyFile, err), "delete the file; Xibalba creates a new key at the next start")
		}
	case errors.Is(err, os.ErrNotExist):
		// The file is created at start-up. Its directory must be there.
		if parent, perr := os.Stat(filepath.Dir(path)); perr != nil || !parent.IsDir() {
			add("challenge.key_file", fmt.Sprintf("the directory %q does not exist", filepath.Dir(c.KeyFile)),
				"create the directory and make it writable for the user Xibalba runs as; the key file itself is created at the first start")
		}
	default:
		add("challenge.key_file", fmt.Sprintf("%q cannot be checked", c.KeyFile), "check the path and its permissions")
	}
}

// isCookieName reports whether s is a conservative, portable cookie name.
func isCookieName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', ch == '-', ch == '_':
		default:
			return false
		}
	}
	return true
}

// Pages holds the settings of the pages Xibalba itself shows to visitors
// ("request blocked", "website unavailable").
type Pages struct {
	// Operator is who runs the website, as it should appear in a sentence.
	// Empty keeps the neutral phrase "The operator of this website".
	// Needs a sponsor license.
	Operator string `yaml:"operator"`
	// Contact says how to reach the operator. Shown on the block page.
	Contact string `yaml:"contact"`
	// ImprintURL and PrivacyURL are linked at the bottom of every page.
	ImprintURL string `yaml:"imprint_url"`
	PrivacyURL string `yaml:"privacy_url"`
	// DefaultLanguage is used when the visitor's browser states no
	// supported language.
	DefaultLanguage string `yaml:"default_language"`
	// Texts replaces individual texts: language, then text name, then text.
	// Needs a sponsor license.
	Texts map[string]map[string]string `yaml:"texts"`
	// Attribution shows the line "Protected by Xibalba" at the bottom of
	// every page. Switching it off needs a sponsor license.
	Attribution bool `yaml:"attribution"`
	// Status is the HTTP status the pages are sent with.
	Status PageStatus `yaml:"status"`
}

// PageStatus holds the HTTP status of the pages a visitor is stopped with.
type PageStatus struct {
	// Challenge is the status of the security check.
	Challenge int `yaml:"challenge"`
	// Blocked is the status of "request blocked".
	Blocked int `yaml:"blocked"`
}

// Options returns the settings exactly as written, in the form
// internal/pages takes them. It is used to validate them. What takes effect
// depends on the sponsor license; see Config.PageOptions.
func (p Pages) Options() pages.Options {
	return pages.Options{
		Operator:        p.Operator,
		Contact:         p.Contact,
		ImprintURL:      p.ImprintURL,
		PrivacyURL:      p.PrivacyURL,
		DefaultLanguage: p.DefaultLanguage,
		Texts:           p.Texts,
		HideAttribution: !p.Attribution,
		StatusChallenge: p.Status.Challenge,
		StatusBlocked:   p.Status.Blocked,
	}
}

// Ops holds the settings of the operations listener.
type Ops struct {
	// Listen is the host:port the listener binds to.
	Listen string `yaml:"listen"`
}

// Allowed values, exported so documentation tests and the future web interface
// can show the same lists the validator enforces.
var (
	LogLevels  = []string{"debug", "info", "warn", "error"}
	LogFormats = []string{"json", "text"}
)

// Default returns the configuration used for every setting the file leaves out.
// The defaults are safe: both listeners are reachable from this machine only
// and no forwarding header is trusted. upstream.url has no default and must be set.
func Default() Config {
	return Config{
		Log: Log{Level: "info", Format: "json"},
		Server: Server{
			Listen:            "127.0.0.1:8080",
			TrustedProxies:    []string{},
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       90 * time.Second,
		},
		Upstream: Upstream{
			URL:                   "", // required: there is no sensible default
			PreserveHost:          true,
			DialTimeout:           5 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
		},
		Rules:      defaultRules(),
		Crawlers:   defaultCrawlers(),
		Limits:     defaultLimits(),
		Admin:      defaultAdmin(),
		Trap:       defaultTrap(),
		Previews:   defaultPreviews(),
		Countries:  defaultCountries(),
		Statistics: defaultStatistics(),
		Challenge: Challenge{
			Difficulty:        18,
			Method:            "pow",
			Checks:            []string{},
			NoJavaScript:      "button",
			Wait:              3 * time.Second,
			ChallengeLifetime: 5 * time.Minute,
			PassLifetime:      168 * time.Hour,
			BindNetwork:       true,
			KeyFile:           "",
			CookieName:        "xibalba-pass",
		},
		Pages: Pages{DefaultLanguage: pages.Languages()[0], Texts: map[string]map[string]string{}, Attribution: true,
			Status: PageStatus{Challenge: http.StatusForbidden, Blocked: http.StatusForbidden}},
		Ops:             Ops{Listen: "127.0.0.1:9090"},
		ShutdownTimeout: 10 * time.Second,
	}
}

// Load reads and validates the configuration file at path.
func Load(path string) (Config, error) { return LoadWith(path, DefaultEnv()) }

// LoadWith is Load with an explicit environment.
func LoadWith(path string, env Env) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, &Error{File: path, Problems: []Problem{{
				Message: "the configuration file does not exist",
				Hint:    "copy xibalba.example.yaml to this path, or pass another file with -config",
			}}}
		}
		return Config{}, &Error{File: path, Problems: []Problem{{
			Message: "the configuration file cannot be read: " + err.Error(),
		}}}
	}
	return ParseWith(path, data, env)
}

// Parse validates configuration data. name is used in error messages, and its
// directory is where relative rule file names are looked up. Settings that
// are left out keep their defaults.
func Parse(name string, data []byte) (Config, error) {
	return ParseWith(name, data, DefaultEnv())
}

// ParseWith is Parse with an explicit environment.
func ParseWith(name string, data []byte, env Env) (Config, error) {
	cfg := Default()

	if hasContent(data) {
		if err := yaml.UnmarshalWithOptions(data, &cfg, yaml.Strict()); err != nil {
			return Config{}, &Error{File: name, Problems: []Problem{{
				Message: "the file is not valid: " + strings.TrimSpace(yaml.FormatError(err, false, true)),
				Hint:    "every setting and its spelling is listed in docs/CONFIGURATION.md",
			}}}
		}
	}

	lines := lineIndex(data)
	problems := cfg.validate(lines)
	cfg.Challenge.check(filepath.Dir(name), func(path, message, hint string) {
		problems = append(problems, Problem{Path: path, Line: nearestLine(lines, path), Message: message, Hint: hint})
	})
	cfg.Trap.check(func(path, message, hint string) {
		problems = append(problems, Problem{Path: path, Line: nearestLine(lines, path), Message: message, Hint: hint})
	})
	cfg.Rules.TrapOn = cfg.Trap.Enabled
	cfg.Statistics.check(filepath.Dir(name), func(path, message, hint string) {
		problems = append(problems, Problem{Path: path, Line: nearestLine(lines, path), Message: message, Hint: hint})
	})
	cfg.Countries.check(filepath.Dir(name), func(path, message, hint string) {
		problems = append(problems, Problem{Path: path, Line: nearestLine(lines, path), Message: message, Hint: hint})
	})
	cfg.Rules.CountriesOn = cfg.Countries.Database != ""
	cfg.Admin.check(filepath.Dir(name), map[string]string{"server.listen": cfg.Server.Listen, "ops.listen": cfg.Ops.Listen}, func(path, message, hint string) {
		problems = append(problems, Problem{Path: path, Line: nearestLine(lines, path), Message: message, Hint: hint})
	})
	cfg.checkVerdict(func(path, message, hint string) {
		problems = append(problems, Problem{Path: path, Line: nearestLine(lines, path), Message: message, Hint: hint})
	})
	cfg.Previews.check(func(path, message, hint string) {
		problems = append(problems, Problem{Path: path, Line: nearestLine(lines, path), Message: message, Hint: hint})
	})
	cfg.Limits.check(func(path, message, hint string) {
		problems = append(problems, Problem{Path: path, Line: nearestLine(lines, path), Message: message, Hint: hint})
	})
	cfg.checkLicense(filepath.Dir(name), env, func(path, message, hint string) {
		problems = append(problems, Problem{Path: path, Line: nearestLine(lines, path), Message: message, Hint: hint})
	})
	problems = append(problems, cfg.Crawlers.load(filepath.Dir(name), lines)...)
	cfg.Rules.Catalog = catalog(cfg.Crawlers.Definitions)
	problems = append(problems, cfg.Rules.load(filepath.Dir(name), lines)...)
	if len(problems) == 0 { // the changes sit on top of a rule set that works by itself
		cfg.checkChanges(filepath.Dir(name), func(path, message, hint string) {
			problems = append(problems, Problem{Path: path, Line: nearestLine(lines, path), Message: message, Hint: hint})
		})
	}
	if len(problems) > 0 {
		return Config{}, &Error{File: name, Problems: problems}
	}
	return cfg, nil
}

// hasContent reports whether data holds any settings. A file that is empty or
// contains only comments is valid and means "use the defaults"; it must not be
// handed to the decoder, which would reset the defaults to zero values.
func hasContent(data []byte) bool {
	file, err := parser.ParseBytes(data, 0)
	if err != nil {
		return true // let the decoder produce the error message
	}
	for _, doc := range file.Docs {
		if doc == nil || doc.Body == nil {
			continue
		}
		switch doc.Body.(type) {
		case *ast.NullNode, *ast.CommentGroupNode:
			continue
		}
		return true
	}
	return false
}

// validate checks every setting and returns all problems found.
func (c Config) validate(lines map[string]int) []Problem {
	var problems []Problem
	add := func(path, message, hint string) {
		problems = append(problems, Problem{Path: path, Line: nearestLine(lines, path), Message: message, Hint: hint})
	}

	if !contains(LogLevels, c.Log.Level) {
		add("log.level", fmt.Sprintf("%q is not a log level", c.Log.Level),
			"use one of: "+strings.Join(LogLevels, ", "))
	}
	if !contains(LogFormats, c.Log.Format) {
		add("log.format", fmt.Sprintf("%q is not a log format", c.Log.Format),
			"use one of: "+strings.Join(LogFormats, ", "))
	}
	if err := checkListen(c.Server.Listen); err != nil {
		add("server.listen", fmt.Sprintf("%q is not a listen address: %v", c.Server.Listen, err),
			`use host:port, for example "127.0.0.1:8080"`)
	}
	for i, entry := range c.Server.TrustedProxies {
		if _, err := parsePrefix(entry); err != nil {
			add(fmt.Sprintf("server.trusted_proxies[%d]", i),
				fmt.Sprintf("%q is not an IP address or network", entry),
				`use an address such as "10.0.0.5" or a network such as "10.0.0.0/8"`)
		}
	}
	if c.Server.ReadHeaderTimeout <= 0 {
		add("server.read_header_timeout", fmt.Sprintf("%s must be greater than zero", c.Server.ReadHeaderTimeout),
			`use a duration such as "10s"`)
	}
	if c.Server.IdleTimeout <= 0 {
		add("server.idle_timeout", fmt.Sprintf("%s must be greater than zero", c.Server.IdleTimeout),
			`use a duration such as "90s"`)
	}
	if c.Upstream.URL == "" && c.Verdict.Enabled {
		// Verdicts only: nothing is passed on.
	} else if c.Upstream.URL == "" {
		add("upstream.url", "the address of the website to protect is missing",
			`set it to where your website is reached, for example "http://127.0.0.1:3000"`)
	} else if _, err := parseUpstream(c.Upstream.URL); err != nil {
		add("upstream.url", fmt.Sprintf("%s is not usable: %v", displayURL(c.Upstream.URL), err),
			`use a URL such as "http://127.0.0.1:3000" or "https://intern.example.org"`)
	}
	if c.Upstream.DialTimeout <= 0 {
		add("upstream.dial_timeout", fmt.Sprintf("%s must be greater than zero", c.Upstream.DialTimeout),
			`use a duration such as "5s"`)
	}
	if c.Upstream.ResponseHeaderTimeout <= 0 {
		add("upstream.response_header_timeout", fmt.Sprintf("%s must be greater than zero", c.Upstream.ResponseHeaderTimeout),
			`use a duration such as "60s"`)
	}
	if c.Pages.Status.Challenge == 0 {
		add("pages.status.challenge", "0 is not an HTTP status", "use 403, or leave the setting out")
	}
	if c.Pages.Status.Blocked == 0 {
		add("pages.status.blocked", "0 is not an HTTP status", "use 403, or leave the setting out")
	}
	for _, p := range pages.Check(c.Pages.Options()) {
		add("pages."+p.Field, p.Message, p.Hint)
	}
	if c.Server.Listen == c.Ops.Listen && !strings.HasSuffix(c.Server.Listen, ":0") {
		add("ops.listen", fmt.Sprintf("%q is already used by server.listen", c.Ops.Listen),
			"give the two listeners different ports")
	}
	if err := checkListen(c.Ops.Listen); err != nil {
		add("ops.listen", fmt.Sprintf("%q is not a listen address: %v", c.Ops.Listen, err),
			`use host:port, for example "127.0.0.1:9090"`)
	}
	if c.ShutdownTimeout <= 0 {
		add("shutdown_timeout", fmt.Sprintf("%s must be greater than zero", c.ShutdownTimeout),
			`use a duration such as "10s" or "1m"`)
	}
	return problems
}

// checkListen reports whether addr is a usable host:port pair.
func checkListen(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return errors.New("it must have the form host:port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return errors.New("the port must be a number from 0 to 65535")
	}
	return nil
}

// parsePrefix reads a trusted-proxy entry: a single address or a network in
// CIDR notation. A single address becomes a network that contains only it.
func parsePrefix(entry string) (netip.Prefix, error) {
	entry = strings.TrimSpace(entry)
	if prefix, err := netip.ParsePrefix(entry); err == nil {
		return prefix.Masked(), nil
	}
	addr, err := netip.ParseAddr(entry)
	if err != nil {
		return netip.Prefix{}, err
	}
	addr = addr.Unmap().WithZone("")
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

// parseUpstream checks that raw is an absolute http or https URL with a host
// and without a query or fragment.
func parseUpstream(raw string) (*url.URL, error) {
	target, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("it is not a URL")
	}
	switch {
	case target.Scheme != "http" && target.Scheme != "https":
		return nil, errors.New(`it must start with "http://" or "https://"`)
	case target.Host == "":
		return nil, errors.New("it has no host")
	case target.RawQuery != "" || target.Fragment != "":
		return nil, errors.New("it must not contain a query or fragment")
	case target.User != nil:
		return nil, errors.New("it must not contain a user name or password")
	}
	return target, nil
}

// displayURL returns raw in a form that is safe to print: a password in the
// URL is replaced, and a value that cannot be parsed is not echoed at all,
// because it might contain one.
func displayURL(raw string) string {
	target, err := url.Parse(raw)
	if err != nil {
		return "the value"
	}
	return strconv.Quote(target.Redacted())
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

// lineIndex maps setting paths such as "log.level" to the line they are on, so
// validation errors can point at the exact place in the file. If the file
// cannot be parsed the index is empty and errors simply carry no line.
func lineIndex(data []byte) map[string]int {
	idx := map[string]int{}
	file, err := parser.ParseBytes(data, 0)
	if err != nil {
		return idx
	}
	for _, doc := range file.Docs {
		if doc != nil && doc.Body != nil {
			walk(doc.Body, "", idx)
		}
	}
	return idx
}

func walk(n ast.Node, prefix string, idx map[string]int) {
	switch v := n.(type) {
	case *ast.MappingNode:
		for _, pair := range v.Values {
			walk(pair, prefix, idx)
		}
	case *ast.MappingValueNode:
		key := v.Key.String()
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if tok := v.Key.GetToken(); tok != nil && tok.Position != nil {
			idx[path] = tok.Position.Line
		}
		walk(v.Value, path, idx)
	case *ast.SequenceNode:
		for i, item := range v.Values {
			path := fmt.Sprintf("%s[%d]", prefix, i)
			if tok := item.GetToken(); tok != nil && tok.Position != nil {
				idx[path] = tok.Position.Line
			}
			walk(item, path, idx)
		}
	}
}

// Problem is one thing that is wrong with the configuration.
type Problem struct {
	// File is the imported file the problem is in. Empty for the
	// configuration file itself.
	File string
	// Path is the setting, for example "log.level". Empty for file-level problems.
	Path string
	// Line is the line of the setting in the file, or 0 if it is not in the file.
	Line int
	// Message says what is wrong.
	Message string
	// Hint says how to fix it.
	Hint string
}

// Error reports every problem found in one configuration file and the files
// it imports.
type Error struct {
	File     string
	Problems []Problem
}

func (e *Error) Error() string {
	var b strings.Builder
	noun := "problems"
	if len(e.Problems) == 1 {
		noun = "problem"
	}
	fmt.Fprintf(&b, "configuration %s: %d %s", e.File, len(e.Problems), noun)
	for _, p := range e.Problems {
		b.WriteString("\n  - ")
		if p.File != "" {
			fmt.Fprintf(&b, "in %s, ", p.File)
		}
		switch {
		case p.Path != "" && p.Line > 0:
			fmt.Fprintf(&b, "line %d, %s: ", p.Line, p.Path)
		case p.Path != "":
			fmt.Fprintf(&b, "%s: ", p.Path)
		}
		b.WriteString(p.Message)
		if p.Hint != "" {
			b.WriteString("\n    fix: " + p.Hint)
		}
	}
	return b.String()
}
