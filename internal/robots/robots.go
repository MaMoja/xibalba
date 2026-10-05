// Package robots turns a robots.txt file into Xibalba rules.
//
// A robots.txt is a request: it says which paths crawlers should stay out
// of, and well-behaved crawlers comply. The rules made here turn the
// request into something that is enforced: what the file disallows gets
// the security check, or is refused. The output is a rule file for
// rules.files; a person reads it before it is used.
package robots

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

// Limits on the input.
const (
	// MaxSize is how much of a robots.txt is read.
	MaxSize = 512 << 10
	// MaxRules is how many rules are written at most.
	MaxRules  = 500
	maxLine   = 2048
	maxAgent  = 100
	maxPrefix = 40 // of the rule names
)

// Options says how the rules are to be made.
type Options struct {
	// Action is what happens to requests for a path the file disallows:
	// "challenge" or "deny". Empty means "challenge".
	Action string
	// AgentAction is what happens to a crawler the file shuts out of the
	// whole site ("Disallow: /"): "deny" or "challenge". Empty means "deny".
	AgentAction string
	// Prefix starts the name of every rule. Empty means "robots".
	Prefix string
}

type group struct {
	agents   []string
	disallow []string
	allow    []string
}

var plainName = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

// Convert reads a robots.txt from r and writes a rule file to w. notes
// tells the person who runs it what was left out or deserves a look.
func Convert(r io.Reader, w io.Writer, opts Options) (notes []string, err error) {
	if opts.Action == "" {
		opts.Action = "challenge"
	}
	if opts.AgentAction == "" {
		opts.AgentAction = "deny"
	}
	if opts.Prefix == "" {
		opts.Prefix = "robots"
	}
	for name, value := range map[string]string{"the action": opts.Action, "the action for crawlers": opts.AgentAction} {
		if value != "challenge" && value != "deny" {
			return nil, fmt.Errorf("%s is %q; it has to be challenge or deny", name, value)
		}
	}
	if !plainName.MatchString(opts.Prefix) || len(opts.Prefix) > maxPrefix {
		return nil, fmt.Errorf("the prefix %q cannot start a rule name; use small letters, digits and -", opts.Prefix)
	}

	groups, notes, err := parse(r)
	if err != nil {
		return nil, err
	}

	var out strings.Builder
	out.WriteString("# Made from a robots.txt by \"xibalba -robots\". Read it before you use it:\n")
	out.WriteString("# a robots.txt asks, these rules enforce. Import it with rules.files.\n")
	out.WriteString("rules:\n")
	n := 0
	rule := func(lines ...string) bool {
		if n >= MaxRules {
			return false
		}
		n++
		fmt.Fprintf(&out, "  - name: %s-%d\n", opts.Prefix, n)
		for _, line := range lines {
			out.WriteString("    " + line + "\n")
		}
		return true
	}
	full := false
	for _, g := range groups {
		everyone := false
		for _, agent := range g.agents {
			everyone = everyone || agent == "*"
		}
		var named []string
		for _, agent := range g.agents {
			if agent != "*" {
				named = append(named, agent)
			}
		}
		sort.Strings(named)
		for _, path := range g.disallow {
			exceptions := exceptionsFor(path, g.allow)
			whole := path == "/" && len(exceptions) == 0
			// The path, for everyone the group is for.
			if everyone {
				if whole {
					notes = append(notes, fmt.Sprintf("the file disallows the whole site for all crawlers; the rule %s-%d gives every request the action %s. Remove it if that is not what you want.", opts.Prefix, n+1, opts.Action))
				}
				lines := []string{"match:"}
				lines = append(lines, indent(pathLines(path, exceptions), "  ")...)
				lines = append(lines, "action: "+opts.Action)
				full = !rule(lines...) || full
				continue
			}
			for _, agent := range named {
				lines := []string{"match:", "  user_agent: {contains: " + quote(agent) + "}"}
				action := opts.Action
				if whole {
					action = opts.AgentAction // shut out of everything: the file's "go away"
				} else {
					lines = append(lines, indent(pathLines(path, exceptions), "  ")...)
				}
				lines = append(lines, "action: "+action)
				full = !rule(lines...) || full
			}
		}
	}
	if full {
		notes = append(notes, fmt.Sprintf("the file gives more than %d rules; the rest was left out", MaxRules))
	}
	if n == 0 {
		return notes, fmt.Errorf("the file disallows nothing, so there are no rules to write")
	}
	notes = append(notes, "rules on a crawler's name stop crawlers that say who they are; one that lies about its name is not stopped by them")
	_, err = io.WriteString(w, out.String())
	return notes, err
}

// parse reads the groups of a robots.txt.
func parse(r io.Reader) (groups []group, notes []string, err error) {
	scanner := bufio.NewScanner(io.LimitReader(r, MaxSize+1))
	scanner.Buffer(make([]byte, 0, maxLine), maxLine)
	var current *group
	inAgents := false // the last line was a User-agent line
	read, skipped, delays := 0, 0, 0
	for scanner.Scan() {
		line := scanner.Text()
		read += len(line) + 1
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
		switch key {
		case "user-agent":
			if !inAgents || current == nil {
				groups = append(groups, group{})
				current = &groups[len(groups)-1]
			}
			inAgents = true
			if value == "" || len(value) > maxAgent || strings.ContainsAny(value, "\"\\") || !printable(value) {
				skipped++
				continue
			}
			current.agents = append(current.agents, value)
		case "disallow", "allow":
			inAgents = false
			if current == nil || value == "" { // "Disallow:" with nothing allows everything
				continue
			}
			if !strings.HasPrefix(value, "/") || !printable(value) || strings.ContainsAny(value, "\"\\") {
				skipped++
				continue
			}
			if key == "disallow" {
				current.disallow = append(current.disallow, value)
			} else {
				current.allow = append(current.allow, value)
			}
		case "crawl-delay":
			inAgents = false
			delays++
		default:
			inAgents = false
		}
	}
	if scanner.Err() != nil || read > MaxSize {
		return nil, nil, fmt.Errorf("the file is larger than %d KiB, has a line longer than %d characters, or cannot be read", MaxSize>>10, maxLine)
	}
	if skipped > 0 {
		notes = append(notes, fmt.Sprintf("%d lines were left out: a name or path with characters that cannot be right", skipped))
	}
	if delays > 0 {
		notes = append(notes, "Crawl-delay lines were left out; request limits (limits) do that job")
	}
	return groups, notes, nil
}

func printable(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// exceptionsFor returns the Allow paths that lie inside a Disallow path:
// what the file takes out of it again.
func exceptionsFor(path string, allow []string) []string {
	var out []string
	base := strings.TrimRight(strings.SplitN(path, "*", 2)[0], "$")
	for _, a := range allow {
		if len(a) > len(base) && strings.HasPrefix(a, base) {
			out = append(out, a)
		}
	}
	return out
}

// pathLines writes the condition for one path of a robots.txt: a prefix,
// or with "*" (anything) and "$" (the end) a regular expression.
func pathLines(path string, exceptions []string) []string {
	lines := []string{"path: " + pathTest(path)}
	if len(exceptions) == 1 {
		lines = append(lines, "not:", "  path: "+pathTest(exceptions[0]))
	} else if len(exceptions) > 1 {
		lines = append(lines, "not:", "  any:")
		for _, e := range exceptions {
			lines = append(lines, "    - path: "+pathTest(e))
		}
	}
	return lines
}

func pathTest(path string) string {
	if !strings.ContainsAny(path, "*$") {
		return "{prefix: " + quote(path) + "}"
	}
	end := strings.HasSuffix(path, "$")
	parts := strings.Split(strings.TrimSuffix(path, "$"), "*")
	for i, part := range parts {
		parts[i] = regexp.QuoteMeta(part)
	}
	expr := "^" + strings.Join(parts, ".*")
	if end {
		expr += "$"
	}
	return "{regex: " + quote(expr) + "}"
}

// quote writes s as a YAML text in single quotes, in which only the quote
// itself has to be doubled.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func indent(lines []string, by string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = by + line
	}
	return out
}
