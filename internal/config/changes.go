package config

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"

	"github.com/MaMoja/xibalba/data"
	"github.com/MaMoja/xibalba/internal/challenge"
	"github.com/MaMoja/xibalba/internal/changes"
	"github.com/MaMoja/xibalba/internal/rules"
)

// Names of the two rules made from the addresses listed in the web
// interface. They come before every other rule.
const (
	ListAllowRule = "web-interface.allow"
	ListDenyRule  = "web-interface.deny"
)

// presetOrder is where a preset switched on in the web interface goes among
// the others: wanted programs first, unwanted crawlers next, wanted crawlers
// after them, and the general check for browsers last.
var presetOrder = []string{
	"keep-internet-working", "allow-feeds", "allow-git-clients", "allow-registry-clients",
	"block-fake-crawlers", "block-trapped", "block-ai-training", "block-archive-crawlers",
	"allow-search-engines", "allow-ai-search", "allow-ai-user-fetch",
	"weigh-odd-browsers", "challenge-browsers",
}

// PresetsInOrder returns every preset that ships with Xibalba, in the order
// in which they are best applied.
func PresetsInOrder() []string {
	out := append([]string(nil), presetOrder...)
	for _, name := range PresetNames() { // one that is missing from the order above goes last
		if presetRank(name) == len(presetOrder) {
			out = append(out, name)
		}
	}
	return out
}

func presetRank(name string) int {
	for i, n := range presetOrder {
		if n == name {
			return i
		}
	}
	return len(presetOrder)
}

// EffectivePresets returns the presets in force: those of the configuration
// file in their order, without the ones switched off in the web interface,
// and with the ones switched on there put at their usual place.
func (r Rules) EffectivePresets(state changes.State) []string {
	var out []string
	listed := map[string]bool{}
	for _, name := range r.Presets {
		listed[name] = true
		if on, changed := state.Presets[name]; !changed || on {
			out = append(out, name)
		}
	}
	for _, name := range presetOrder { // a fixed order, so the result does not depend on map order
		if !state.Presets[name] || listed[name] {
			continue
		}
		at := len(out)
		for i, other := range out {
			if presetRank(other) > presetRank(name) {
				at = i
				break
			}
		}
		out = append(out[:at], append([]string{name}, out[at:]...)...)
	}
	return out
}

// presetRules reads the rules of a preset that ships with Xibalba.
func presetRules(name string) ([]rules.RuleSpec, []byte, error) {
	content, err := fs.ReadFile(data.Files, "presets/"+name+".yaml")
	var doc ruleFileDoc
	if err == nil {
		err = yaml.UnmarshalWithOptions(content, &doc, yaml.Strict())
	}
	return doc.Rules, content, err
}

// RuleSpec returns the rule set in force at now: the configuration file
// with the changes made in the web interface on top.
func (c *Config) RuleSpec(state changes.State, now time.Time) (rules.Spec, error) {
	spec, _, _, err := c.ruleSpec(state, now)
	return spec, err
}

// ruleSpec is RuleSpec that also says where the rules written in the web
// interface lie in the result (from index first, count of them) so that a
// problem in one of them can be reported with its line.
func (c *Config) ruleSpec(state changes.State, now time.Time) (spec rules.Spec, first, count int, err error) {
	var all []rules.RuleSpec
	allow, deny := state.Active(now)
	// Blocked comes first: where a blocked address lies inside a network
	// that is let through, the block holds.
	if len(deny) > 0 {
		all = append(all, rules.RuleSpec{Name: ListDenyRule, Match: rules.MatchSpec{IP: deny}, Action: rules.Deny})
	}
	if len(allow) > 0 {
		// The owner listed these addresses by hand: they are let through
		// and, like limits.exempt, not counted by the request limits.
		all = append(all, rules.RuleSpec{Name: ListAllowRule, Match: rules.MatchSpec{IP: allow}, Action: rules.Allow, ExemptFromLimits: true})
	}
	// Rules written in the web interface come before those of the
	// configuration, so that what is changed there has an effect.
	first = len(all)
	if hasContent([]byte(state.Rules)) {
		if err := plainYAML(state.Rules); err != nil {
			return spec, 0, 0, err
		}
		var doc ruleFileDoc
		if err := yaml.UnmarshalWithOptions([]byte(state.Rules), &doc, yaml.Strict()); err != nil {
			return spec, 0, 0, errors.New(strings.TrimSpace(yaml.FormatError(err, false, true)))
		}
		count = len(doc.Rules)
		all = append(all, doc.Rules...)
	}
	all = append(all, c.Rules.List...)
	for _, name := range c.Rules.EffectivePresets(state) {
		preset, _, err := presetRules(name)
		if err != nil {
			return spec, 0, 0, fmt.Errorf("the preset %q cannot be read", name)
		}
		all = append(all, preset...)
	}
	for _, file := range c.Rules.Imported {
		all = append(all, file.Rules...)
	}
	r := c.Rules
	return rules.Spec{DefaultAction: r.DefaultAction, Thresholds: r.Thresholds, Rules: all, Crawlers: r.Catalog, Trap: r.TrapOn, Countries: r.CountriesOn,
		ASN: r.ASNOn, AddressLists: r.Lists}, first, count, nil
}

// plainYAML refuses the parts of YAML that a rule text typed into a web
// page has no use for and that can make a small text expensive: anchors and
// aliases (one line can stand for a million), merge keys, tags, several
// documents, and nesting far deeper than any rule goes.
func plainYAML(text string) error {
	depth, deepest, indent := 0, 0, 0
	for _, line := range strings.Split(text, "\n") {
		indent = max(indent, len(line)-len(strings.TrimLeft(line, " ")))
		for i := 0; i < len(line); i++ {
			switch line[i] {
			case '[', '{':
				depth++
				deepest = max(deepest, depth)
			case ']', '}':
				depth--
			}
		}
	}
	if deepest > 40 || indent > 80 {
		return errors.New("the text is nested deeper than any rule needs")
	}
	file, err := parser.ParseBytes([]byte(text), 0)
	if err != nil {
		return errors.New(strings.TrimSpace(yaml.FormatError(err, false, true)))
	}
	if len(file.Docs) > 1 {
		return errors.New(`the text holds more than one document ("---"); only one is read, so write one`)
	}
	var found string
	for _, doc := range file.Docs {
		ast.Walk(visitorFunc(func(node ast.Node) {
			switch node.(type) {
			case *ast.AnchorNode, *ast.AliasNode, *ast.MergeKeyNode:
				found = `anchors and aliases ("&name", "*name", "<<") are not read here; write the conditions out`
			case *ast.TagNode:
				found = `tags ("!!type") are not read here; leave them out`
			}
		}), doc)
	}
	if found != "" {
		return errors.New(found)
	}
	return nil
}

type visitorFunc func(ast.Node)

func (f visitorFunc) Visit(node ast.Node) ast.Visitor {
	if node != nil {
		f(node)
	}
	return f
}

// Compile builds the rule set in force at now. If it does not work, it
// returns what is wrong, one line per problem; a problem in a rule written
// in the web interface names the line of that text.
func (c *Config) Compile(state changes.State, now time.Time) (engine *rules.Engine, problems []string) {
	// The text comes from a web page or a hand-edited file. Whatever it
	// does to the YAML reader, the answer is a problem, never a crash.
	defer func() {
		if recover() != nil {
			engine, problems = nil, []string{"the rules are not valid: the text cannot be read as YAML"}
		}
	}()
	return c.compile(state, now)
}

func (c *Config) compile(state changes.State, now time.Time) (*rules.Engine, []string) {
	spec, first, count, err := c.ruleSpec(state, now)
	if err != nil {
		return nil, []string{"the rules are not valid: " + err.Error()}
	}
	var out []string
	for i, rule := range spec.Rules[first : first+count] {
		if strings.HasPrefix(rule.Name, "web-interface.") || strings.HasPrefix(rule.Name, "preset.") {
			out = append(out, fmt.Sprintf(`rule number %d: the name %q is kept for Xibalba's own rules; choose a name that does not start with "web-interface." or "preset."`, i+1, rule.Name))
		}
	}
	engine, problems := rules.Compile(spec)
	lines := lineIndex([]byte(state.Rules))
	for _, p := range problems {
		text := p.Message
		if p.Hint != "" {
			text += " (" + p.Hint + ")"
		}
		if p.Rule >= first && p.Rule < first+count {
			path := fmt.Sprintf("rules[%d]", p.Rule-first)
			if p.Field != "" {
				path += "." + p.Field
			}
			text = fmt.Sprintf("line %d, %s: %s", nearestLine(lines, path), path, text)
		}
		out = append(out, text)
	}
	if len(out) > 0 {
		return nil, out
	}
	// A rule may describe its security check in part and take the rest
	// from the default. What comes out together must make sense.
	for _, source := range engine.Sources() {
		if source.Challenge == nil {
			continue
		}
		p := c.Challenge.Profile(source.Challenge)
		switch {
		case len(p.Checks) > 0 && p.Method != challenge.MethodPoW && p.Method != challenge.MethodScript:
			out = append(out, fmt.Sprintf("%s: the security check has extra checks (%s), which run in JavaScript, and the method %s, which does without it (give the rule method pow or script, or checks: [])",
				source.ID, strings.Join(p.Checks, ", "), p.Method))
		case source.Challenge.Difficulty != 0 && p.Method != challenge.MethodPoW:
			out = append(out, fmt.Sprintf("%s: difficulty belongs to the method pow, but the security check uses %s (give the rule method: pow, or remove difficulty)", source.ID, p.Method))
		}
	}
	if len(out) > 0 {
		return nil, out
	}
	return engine, nil
}

// Profile returns the security check a rule asks for: what the rule
// describes, and the default for everything it leaves out. Nil is the default.
func (c Challenge) Profile(spec *rules.ChallengeSpec) challenge.Profile {
	p := challenge.Profile{Method: c.Method, Difficulty: c.Difficulty, Wait: c.Wait,
		Checks: c.Checks, AllowButton: c.NoJavaScript == "button"}
	if spec == nil {
		return p
	}
	if spec.Method != "" {
		p.Method = spec.Method
	}
	if spec.Difficulty != 0 {
		p.Difficulty = spec.Difficulty
	}
	if spec.Wait != 0 {
		p.Wait = spec.Wait
	}
	if spec.Checks != nil {
		p.Checks = spec.Checks
	}
	if spec.NoJavaScript != "" {
		p.AllowButton = spec.NoJavaScript == "button"
	}
	return p
}

// checkChanges reads the changes file and makes sure the configuration with
// the changes on top is a rule set that works.
func (c *Config) checkChanges(dir string, add func(path, message, hint string)) {
	a := &c.Admin
	a.Changes, a.ChangesPath = changes.State{}, ""
	if strings.TrimSpace(a.ChangesFile) == "" {
		add("admin.changes_file", "no file is named", `name the file that keeps the changes made in the web interface, for example "admin.changes.json"`)
		return
	}
	a.ChangesPath = resolve(dir, a.ChangesFile)
	const hint = "correct the file, or delete it to drop every change made in the web interface"
	for i, rule := range c.Rules.List {
		if strings.HasPrefix(rule.Name, "web-interface.") {
			add(fmt.Sprintf("rules.list[%d].name", i), fmt.Sprintf("the name %q is kept for the address list of the web interface", rule.Name),
				`choose a name that does not start with "web-interface."`)
		}
	}
	for _, file := range c.Rules.Imported {
		for _, rule := range file.Rules {
			if strings.HasPrefix(rule.Name, "web-interface.") {
				add("rules.files", fmt.Sprintf("in %s, the name %q is kept for the address list of the web interface", file.Path, rule.Name),
					`choose a name that does not start with "web-interface."`)
			}
		}
	}
	state, err := changes.Load(a.ChangesPath)
	if err != nil {
		add("admin.changes_file", fmt.Sprintf("%q cannot be used: %v", a.ChangesFile, err), hint)
		return
	}
	state = state.WithoutExpired(time.Now()) // no effect any more; gone from the file at the next change
	if err := state.Check(PresetNames()); err != nil {
		add("admin.changes_file", fmt.Sprintf("%q holds a change that is not valid: %v", a.ChangesFile, err), hint)
		return
	}
	if _, problems := c.Compile(state, time.Now()); len(problems) > 0 {
		if state.Empty() {
			add("rules", problems[0], "see docs/CHALLENGE.md for what a rule may ask of the security check")
		} else {
			add("admin.changes_file", fmt.Sprintf("with the changes in %q the rule set does not work: %s", a.ChangesFile, problems[0]), hint)
		}
		return
	}
	a.Changes = state
}
