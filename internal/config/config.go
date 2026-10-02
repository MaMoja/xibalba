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
	"os"
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
// The defaults are safe: the operations listener is reachable from this machine only.
func Default() Config {
	return Config{
		Log:             Log{Level: "info", Format: "json"},
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

// Parse validates configuration data. name is used in error messages only.
// An empty document is valid and yields Default().
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
	if problems := cfg.validate(lines); len(problems) > 0 {
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
		problems = append(problems, Problem{Path: path, Line: lines[path], Message: message, Hint: hint})
	}

	if !contains(LogLevels, c.Log.Level) {
		add("log.level", fmt.Sprintf("%q is not a log level", c.Log.Level),
			"use one of: "+strings.Join(LogLevels, ", "))
	}
	if !contains(LogFormats, c.Log.Format) {
		add("log.format", fmt.Sprintf("%q is not a log format", c.Log.Format),
			"use one of: "+strings.Join(LogFormats, ", "))
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
	// Path is the setting, for example "log.level". Empty for file-level problems.
	Path string
	// Line is the line of the setting in the file, or 0 if it is not in the file.
	Line int
	// Message says what is wrong.
	Message string
	// Hint says how to fix it.
	Hint string
}

// Error reports every problem found in one configuration file.
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
