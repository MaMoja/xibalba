package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/MaMoja/xibalba/data"
	"github.com/MaMoja/xibalba/internal/crawlers"
	"github.com/MaMoja/xibalba/internal/rules"
)

// Limits on imported rule files.
const (
	maxRuleFiles    = 64
	maxRuleFileSize = 1 << 20 // 1 MiB
)

// OnErrorModes are the allowed values of rules.on_error.
var OnErrorModes = []string{"allow", "deny"}

// Rules holds the rule set and how it is enforced. The meaning of rules,
// conditions and actions is documented in docs/RULES.md and implemented in
// internal/rules.
type Rules struct {
	// DryRun evaluates and counts every decision but lets every request through.
	DryRun bool `yaml:"dry_run"`
	// DefaultAction applies when no rule decides: allow, deny or challenge.
	DefaultAction rules.Action `yaml:"default_action"`
	// OnError is what happens to a request if evaluating it fails inside
	// Xibalba: "allow" (fail open) or "deny" (fail closed).
	OnError string `yaml:"on_error"`
	// Thresholds turn a request's score into an action.
	Thresholds []rules.ThresholdSpec `yaml:"thresholds"`
	// Presets names ready-made rule groups that ship with Xibalba. They are
	// evaluated after List and before Files.
	Presets []string `yaml:"presets"`
	// Files lists rule files to import, relative to the configuration file.
	Files []string `yaml:"files"`
	// List holds the rules written directly in the configuration file. They
	// are evaluated first.
	List []rules.RuleSpec `yaml:"list"`

	// AddressLists names files of addresses and networks, one per line,
	// relative to the configuration file. Rules refer to a list by its
	// name with the condition address_list.
	AddressLists map[string]string `yaml:"address_lists"`

	// Lists holds the address lists read from AddressLists. It is filled
	// when the configuration is loaded and is not a setting.
	Lists map[string]*rules.AddressSet `yaml:"-"`
	// ASNOn says whether a database of network operators is configured.
	// It is filled when the configuration is loaded and is not a setting.
	ASNOn bool `yaml:"-"`

	// Preset holds the rules of Presets, in the order of Presets. It is
	// filled when the configuration is loaded and is not a setting.
	Preset []RuleFile `yaml:"-"`
	// Catalog lists the crawlers that rules may refer to. It is filled when
	// the configuration is loaded and is not a setting.
	Catalog *rules.Catalog `yaml:"-"`
	// TrapOn says whether the trap is switched on. It is filled when the
	// configuration is loaded and is not a setting.
	TrapOn bool `yaml:"-"`
	// CountriesOn says whether a country database is configured. It is
	// filled when the configuration is loaded and is not a setting.
	CountriesOn bool `yaml:"-"`

	// Imported holds the rules read from Files, in the order of Files. It is
	// filled when the configuration is loaded and is not a setting.
	Imported []RuleFile `yaml:"-"`
}

// RuleFile is one imported rule file.
type RuleFile struct {
	// Path is the file as written in rules.files.
	Path string
	// Rules are the rules in the file, in order.
	Rules []rules.RuleSpec
}

// ruleFileDoc is the layout of a rule file: one top-level list named "rules".
type ruleFileDoc struct {
	Rules []rules.RuleSpec `yaml:"rules"`
}

func defaultRules() Rules {
	return Rules{
		DryRun:        false,
		DefaultAction: rules.Allow,
		OnError:       "allow",
		Thresholds:    []rules.ThresholdSpec{},
		Presets:       []string{},
		Files:         []string{},
		AddressLists:  map[string]string{},
		List:          []rules.RuleSpec{},
	}
}

// Spec returns the complete rule set in evaluation order: the rules from the
// configuration file first, then the presets, then each imported file in the
// order listed. A rule in the list therefore overrides a preset, and a preset
// is not shadowed by a general rule in an imported file.
func (r Rules) Spec() rules.Spec {
	all := append([]rules.RuleSpec(nil), r.List...)
	for _, preset := range r.Preset {
		all = append(all, preset.Rules...)
	}
	for _, file := range r.Imported {
		all = append(all, file.Rules...)
	}
	return rules.Spec{DefaultAction: r.DefaultAction, Thresholds: r.Thresholds, Rules: all, Crawlers: r.Catalog, Trap: r.TrapOn, Countries: r.CountriesOn,
		ASN: r.ASNOn, AddressLists: r.Lists}
}

// Limits on address lists.
const (
	maxAddressLists    = 32
	maxAddressListSize = 64 << 20 // 64 MiB
)

// loadAddressLists reads the files named in AddressLists into r.Lists.
func (r *Rules) loadAddressLists(dir string, add func(path, message, hint string)) {
	r.Lists = map[string]*rules.AddressSet{}
	if len(r.AddressLists) > maxAddressLists {
		add("rules.address_lists", fmt.Sprintf("%d address lists are too many", len(r.AddressLists)), fmt.Sprintf("give %d at most; several files can be joined into one", maxAddressLists))
		return
	}
	names := make([]string, 0, len(r.AddressLists))
	for name := range r.AddressLists {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		field := "rules.address_lists." + name
		if !listName(name) {
			add(field, "this cannot be the name of an address list", "use 1 to 40 small letters, digits, - and _, for example vpn or data-centres")
			continue
		}
		// A list that cannot be read is reported once, here; the rules
		// that name it are not reported on top of that.
		r.Lists[name] = rules.NewAddressSet(nil)
		file := r.AddressLists[name]
		if strings.TrimSpace(file) == "" {
			add(field, "no file is named", `give the file that holds the addresses, for example "lists/vpn.txt"`)
			continue
		}
		path := file
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		info, err := os.Stat(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			add(field, fmt.Sprintf("the file %q does not exist", file), "create it with one address or network per line; an empty file is a list without entries")
			continue
		case err != nil || !info.Mode().IsRegular():
			add(field, fmt.Sprintf("%q cannot be read or is not a file", file), "give a file the user Xibalba runs as may read")
			continue
		case info.Size() > maxAddressListSize:
			add(field, fmt.Sprintf("%q is larger than %d MiB", file, maxAddressListSize>>20), "shorten the list; networks instead of single addresses take far less room")
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			add(field, fmt.Sprintf("%q cannot be read", file), "give a file the user Xibalba runs as may read")
			continue
		}
		set, problems := rules.ReadAddressList(io.LimitReader(f, maxAddressListSize+1))
		_ = f.Close()
		for _, p := range problems {
			add(field, fmt.Sprintf("%s, line %d: %s", file, p.Line, p.Message),
				`write one address or network per line, such as "192.0.2.7" or "2001:db8::/32"; text after "#" is ignored`)
		}
		if len(problems) == 0 {
			r.Lists[name] = set
		}
	}
}

func listName(name string) bool {
	if name == "" || len(name) > 40 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		letter, digit := c >= 'a' && c <= 'z', c >= '0' && c <= '9'
		if !letter && !digit && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// PresetNames returns the names of the presets that ship with Xibalba, sorted.
func PresetNames() []string {
	entries, _ := fs.ReadDir(data.Files, "presets")
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, strings.TrimSuffix(entry.Name(), ".yaml"))
	}
	sort.Strings(names)
	return names
}

// catalog lists what crawler conditions may refer to.
func catalog(defs []crawlers.Definition) *rules.Catalog {
	c := &rules.Catalog{Names: crawlers.Names(defs)}
	for _, class := range crawlers.Classes {
		c.Classes = append(c.Classes, string(class))
	}
	return c
}

// origin says where a rule in the combined rule set was written.
type origin struct {
	file  string         // "" for the configuration file itself
	path  string         // path of the rule, such as "rules.list[2]" or "rules[0]"
	lines map[string]int // line index of the file the rule is in
}

// load reads the imported rule files into r.Imported, compiles the complete
// rule set to check it, and returns every problem found. dir is the directory
// that relative file names are resolved against; lines is the line index of
// the configuration file.
func (r *Rules) load(dir string, lines map[string]int) []Problem {
	var problems []Problem
	add := func(path, message, hint string) {
		problems = append(problems, Problem{Path: path, Line: nearestLine(lines, path), Message: message, Hint: hint})
	}

	if !contains(OnErrorModes, r.OnError) {
		add("rules.on_error", fmt.Sprintf("%q is not a failure mode", r.OnError),
			"use allow (keep the website reachable) or deny (block until the problem is fixed)")
	}

	r.loadAddressLists(dir, add)

	origins := make([]origin, 0, len(r.List))
	for i := range r.List {
		origins = append(origins, origin{path: fmt.Sprintf("rules.list[%d]", i), lines: lines})
	}

	r.Preset = nil
	known := PresetNames()
	listed := map[string]int{}
	for i, name := range r.Presets {
		field := fmt.Sprintf("rules.presets[%d]", i)
		if !contains(known, name) {
			add(field, fmt.Sprintf("%q is not a preset", name), "use one of: "+strings.Join(known, ", "))
			continue
		}
		if first, dup := listed[name]; dup {
			add(field, fmt.Sprintf("%q is already listed as entry number %d", name, first+1), "list every preset once")
			continue
		}
		listed[name] = i
		content, err := fs.ReadFile(data.Files, "presets/"+name+".yaml")
		var doc ruleFileDoc
		if err == nil {
			err = yaml.UnmarshalWithOptions(content, &doc, yaml.Strict())
		}
		if err != nil {
			add(field, fmt.Sprintf("the preset %q cannot be read: %v", name, err), "this is a fault in Xibalba; please report it")
			continue
		}
		presetLines := lineIndex(content)
		for j := range doc.Rules {
			origins = append(origins, origin{file: "preset " + name, path: fmt.Sprintf("rules[%d]", j), lines: presetLines})
		}
		r.Preset = append(r.Preset, RuleFile{Path: name, Rules: doc.Rules})
	}

	if len(r.Files) > maxRuleFiles {
		add("rules.files", fmt.Sprintf("%d files are listed; the limit is %d", len(r.Files), maxRuleFiles),
			"combine rule files")
	}
	r.Imported = nil
	seen := map[string]int{}
	for i, name := range r.Files {
		if i >= maxRuleFiles {
			break
		}
		field := fmt.Sprintf("rules.files[%d]", i)
		if strings.TrimSpace(name) == "" {
			add(field, "the file name is empty", "give the path of a rule file, or remove the entry")
			continue
		}
		full := name
		if !filepath.IsAbs(full) {
			full = filepath.Join(dir, name)
		}
		full = filepath.Clean(full)
		if first, dup := seen[full]; dup {
			add(field, fmt.Sprintf("%q is already listed as entry number %d", name, first+1), "list every file once")
			continue
		}
		seen[full] = i

		raw, err := readRuleFile(full)
		if err != nil {
			add(field, fmt.Sprintf("the rule file %q cannot be used: %v", name, err),
				"check the path; it is relative to the directory of the configuration file")
			continue
		}
		var doc ruleFileDoc
		if hasContent(raw) {
			if err := yaml.UnmarshalWithOptions(raw, &doc, yaml.Strict()); err != nil {
				problems = append(problems, Problem{
					File:    name,
					Message: "the file is not valid: " + strings.TrimSpace(yaml.FormatError(err, false, true)),
					Hint:    `a rule file has one top-level key "rules" holding a list of rules; see docs/RULES.md`,
				})
				continue
			}
		}
		fileLines := lineIndex(raw)
		for j := range doc.Rules {
			origins = append(origins, origin{file: name, path: fmt.Sprintf("rules[%d]", j), lines: fileLines})
		}
		r.Imported = append(r.Imported, RuleFile{Path: name, Rules: doc.Rules})
	}

	_, ruleProblems := rules.Compile(r.Spec())
	for _, p := range ruleProblems {
		if p.Rule < 0 || p.Rule >= len(origins) {
			path := "rules"
			if p.Field != "" {
				path += "." + p.Field
			}
			add(path, p.Message, p.Hint)
			continue
		}
		o := origins[p.Rule]
		path := o.path
		if p.Field != "" {
			path += "." + p.Field
		}
		problems = append(problems, Problem{
			File: o.file, Path: path, Line: nearestLine(o.lines, path), Message: p.Message, Hint: p.Hint,
		})
	}
	return problems
}

// readRuleFile reads a rule file, refusing anything that is not a regular
// file of reasonable size.
func readRuleFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, errors.New("it does not exist")
	case err != nil:
		return nil, errors.New("it cannot be opened")
	case !info.Mode().IsRegular():
		return nil, errors.New("it is not a regular file")
	case info.Size() > maxRuleFileSize:
		return nil, fmt.Errorf("it is larger than %d KiB", maxRuleFileSize>>10)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("it cannot be read")
	}
	return data, nil
}

// nearestLine returns the line of path, or of the closest enclosing setting
// that appears in the file. A problem with a setting that was left out (a
// rule without a name, say) then still points at the rule it belongs to.
func nearestLine(lines map[string]int, path string) int {
	for path != "" {
		if line, ok := lines[path]; ok {
			return line
		}
		cut := strings.LastIndexAny(path, ".[")
		if cut < 0 {
			return 0
		}
		path = path[:cut]
	}
	return 0
}
