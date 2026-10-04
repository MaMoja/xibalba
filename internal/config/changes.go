package config

import (
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/MaMoja/xibalba/data"
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
	var all []rules.RuleSpec
	allow, deny := state.Active(now)
	if len(allow) > 0 {
		// The owner listed these addresses by hand: they are let through
		// and, like limits.exempt, not counted by the request limits.
		all = append(all, rules.RuleSpec{Name: ListAllowRule, Match: rules.MatchSpec{IP: allow}, Action: rules.Allow, ExemptFromLimits: true})
	}
	if len(deny) > 0 {
		all = append(all, rules.RuleSpec{Name: ListDenyRule, Match: rules.MatchSpec{IP: deny}, Action: rules.Deny})
	}
	all = append(all, c.Rules.List...)
	for _, name := range c.Rules.EffectivePresets(state) {
		preset, _, err := presetRules(name)
		if err != nil {
			return rules.Spec{}, fmt.Errorf("the preset %q cannot be read", name)
		}
		all = append(all, preset...)
	}
	for _, file := range c.Rules.Imported {
		all = append(all, file.Rules...)
	}
	r := c.Rules
	return rules.Spec{DefaultAction: r.DefaultAction, Thresholds: r.Thresholds, Rules: all, Crawlers: r.Catalog, Trap: r.TrapOn, Countries: r.CountriesOn}, nil
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
	state, err := changes.Load(a.ChangesPath)
	if err != nil {
		add("admin.changes_file", fmt.Sprintf("%q cannot be used: %v", a.ChangesFile, err), hint)
		return
	}
	if err := state.Check(PresetNames()); err != nil {
		add("admin.changes_file", fmt.Sprintf("%q holds a change that is not valid: %v", a.ChangesFile, err), hint)
		return
	}
	if state.Empty() {
		return
	}
	spec, err := c.RuleSpec(state, time.Now())
	if err == nil {
		if _, problems := rules.Compile(spec); len(problems) > 0 {
			err = fmt.Errorf("%s", problems[0].Message)
		}
	}
	if err != nil {
		add("admin.changes_file", fmt.Sprintf("with the changes in %q the rule set does not work: %v", a.ChangesFile, err), hint)
		return
	}
	a.Changes = state
}
