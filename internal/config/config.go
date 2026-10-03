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
		Rules:           defaultRules(),
		Ops:             Ops{Listen: "127.0.0.1:9090"},
		ShutdownTimeout: 10 * time.Second,
	}
}

// Load reads and validates the configuration file at path.
func Load(path string) (Config, error) {
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
	return Parse(path, data)
}

// Parse validates configuration data. name is used in error messages, and its
// directory is where relative rule file names are looked up. Settings that
// are left out keep their defaults.
func Parse(name string, data []byte) (Config, error) {
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
	problems = append(problems, cfg.Rules.load(filepath.Dir(name), lines)...)
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
	if c.Upstream.URL == "" {
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
