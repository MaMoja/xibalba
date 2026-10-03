package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MaMoja/xibalba/internal/rules"
)

// writeFiles writes the given files into a fresh directory and returns the
// path of "xibalba.yaml" in it.
func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "xibalba.yaml")
}

func ruleNames(spec rules.Spec) string {
	names := make([]string, len(spec.Rules))
	for i, r := range spec.Rules {
		names[i] = r.Name
	}
	return strings.Join(names, " ")
}

func TestRulesInTheConfigurationFile(t *testing.T) {
	cfg, err := Parse("test.yaml", []byte(minimal+`
rules:
  dry_run: true
  default_action: challenge
  on_error: deny
  thresholds:
    - {weight: 10, action: challenge}
    - {weight: 20, action: deny}
  list:
    - name: block-example-bot
      match:
        user_agent: {contains: "ExampleBot"}
      action: deny
    - name: weigh-no-language
      match:
        header:
          Accept-Language: {present: false}
        not:
          ip: ["192.0.2.0/24"]
      action: weigh
      weight: 5
`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	r := cfg.Rules
	if !r.DryRun || r.DefaultAction != rules.Challenge || r.OnError != "deny" || len(r.Thresholds) != 2 {
		t.Errorf("settings not read: %+v", r)
	}
	if got := ruleNames(r.Spec()); got != "block-example-bot weigh-no-language" {
		t.Errorf("rules = %q", got)
	}
	second := r.List[1]
	if second.Action != rules.Weigh || second.Weight != 5 || second.Match.Not == nil ||
		second.Match.Header["Accept-Language"].Present == nil || *second.Match.Header["Accept-Language"].Present {
		t.Errorf("second rule not read correctly: %+v", second)
	}
	if _, problems := rules.Compile(r.Spec()); len(problems) > 0 {
		t.Errorf("a loaded rule set must compile: %+v", problems)
	}
}

func TestRuleProblemsPointAtTheLine(t *testing.T) {
	tests := []struct {
		name  string
		rules string // appended after minimal (2 lines), so "rules:" is line 3
		want  []string
	}{
		{
			name:  "bad default action",
			rules: "rules:\n  default_action: block\n",
			want:  []string{"line 4, rules.default_action", `"block"`, "allow, deny, challenge"},
		},
		{
			name:  "bad failure mode",
			rules: "rules:\n  on_error: open\n",
			want:  []string{"line 4, rules.on_error", "allow (keep the website reachable)"},
		},
		{
			name:  "bad threshold",
			rules: "rules:\n  thresholds:\n    - {weight: 10, action: challenge}\n    - {weight: 0, action: deny}\n",
			want:  []string{"line 6, rules.thresholds[1].weight", "out of range"},
		},
		{
			name: "bad regex in the second rule",
			rules: "rules:\n  list:\n    - name: a\n      match:\n        path: {prefix: \"/x\"}\n      action: deny\n" +
				"    - name: b\n      match:\n        user_agent:\n          regex: \"(unclosed\"\n      action: deny\n",
			want: []string{"line 12, rules.list[1].match.user_agent.regex", "not valid", "RE2"},
		},
		{
			name:  "rule without a name points at the rule",
			rules: "rules:\n  list:\n    - match:\n        path: {prefix: \"/x\"}\n      action: deny\n",
			want:  []string{"line 5, rules.list[0].name", "no name"},
		},
		{
			name:  "bad address in a list",
			rules: "rules:\n  list:\n    - name: a\n      match:\n        ip:\n          - 10.0.0.0/8\n          - nonsense\n      action: deny\n",
			want:  []string{"line 9, rules.list[0].match.ip[1]", `"nonsense"`},
		},
		{
			name:  "header named in the file",
			rules: "rules:\n  list:\n    - name: a\n      match:\n        header:\n          X-Forwarded-For: {contains: \"10.\"}\n      action: allow\n",
			want:  []string{"line 8, rules.list[0].match.header.X-Forwarded-For", "cannot be trusted", "ip condition"},
		},
		{
			name:  "misspelled key inside a rule",
			rules: "rules:\n  list:\n    - name: a\n      match:\n        useragent: {contains: \"x\"}\n      action: deny\n",
			want:  []string{"not valid", "useragent"},
		},
		{
			name:  "missing rule file",
			rules: "rules:\n  files:\n    - nowhere.yaml\n",
			want:  []string{"line 5, rules.files[0]", `"nowhere.yaml"`, "does not exist"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFiles(t, map[string]string{"xibalba.yaml": minimal + tt.rules})
			_, err := Load(path)
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

func TestRuleFilesAreImportedAfterTheList(t *testing.T) {
	path := writeFiles(t, map[string]string{
		"xibalba.yaml": minimal + `
rules:
  files:
    - sets/first.yaml
    - second.yaml
  list:
    - name: mine
      match: {path: {prefix: "/mine"}}
      action: allow
`,
		"sets/first.yaml": "rules:\n  - name: first-a\n    match: {path: {prefix: \"/a\"}}\n    action: deny\n" +
			"  - name: first-b\n    match: {path: {prefix: \"/b\"}}\n    action: deny\n",
		"second.yaml": "# only a comment, no rules yet\n",
	})
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := ruleNames(cfg.Rules.Spec()); got != "mine first-a first-b" {
		t.Errorf("evaluation order = %q, want the list first, then the files in order", got)
	}
	if len(cfg.Rules.Imported) != 2 || cfg.Rules.Imported[0].Path != "sets/first.yaml" || len(cfg.Rules.Imported[1].Rules) != 0 {
		t.Errorf("imported = %+v", cfg.Rules.Imported)
	}
}

func TestProblemsInRuleFilesNameTheFileAndLine(t *testing.T) {
	tests := []struct {
		name string
		file string
		want []string
	}{
		{
			name: "bad action",
			file: "rules:\n  - name: a\n    match: {path: {prefix: \"/a\"}}\n    action: deny\n  - name: b\n    match: {path: {prefix: \"/b\"}}\n    action: block\n",
			want: []string{"in extra.yaml, line 7, rules[1].action", `"block" is not an action`},
		},
		{
			name: "name already used in the configuration file",
			file: "rules:\n  - name: mine\n    match: {path: {prefix: \"/a\"}}\n    action: deny\n",
			want: []string{"in extra.yaml, line 2, rules[0].name", "already used by rule number 1"},
		},
		{
			name: "wrong layout",
			file: "- name: a\n  action: deny\n",
			want: []string{"in extra.yaml, the file is not valid", `top-level key "rules"`},
		},
		{
			name: "unknown key",
			file: "rules:\n  - name: a\n    when: {path: {prefix: \"/a\"}}\n    action: deny\n",
			want: []string{"in extra.yaml, the file is not valid", "when"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFiles(t, map[string]string{
				"xibalba.yaml": minimal + "rules:\n  files: [extra.yaml]\n  list:\n    - name: mine\n      match: {path: {prefix: \"/mine\"}}\n      action: allow\n",
				"extra.yaml":   tt.file,
			})
			_, err := Load(path)
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

func TestRuleFileLimits(t *testing.T) {
	t.Run("listed twice", func(t *testing.T) {
		path := writeFiles(t, map[string]string{
			"xibalba.yaml": minimal + "rules:\n  files: [a.yaml, ./a.yaml]\n",
			"a.yaml":       "rules: []\n",
		})
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "already listed as entry number 1") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("a directory", func(t *testing.T) {
		path := writeFiles(t, map[string]string{
			"xibalba.yaml": minimal + "rules:\n  files: [sets]\n",
			"sets/a.yaml":  "rules: []\n",
		})
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("too large", func(t *testing.T) {
		path := writeFiles(t, map[string]string{
			"xibalba.yaml": minimal + "rules:\n  files: [big.yaml]\n",
			"big.yaml":     "# " + strings.Repeat("x", maxRuleFileSize) + "\n",
		})
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "larger than") {
			t.Errorf("got %v", err)
		}
	})
}

func TestNearestLine(t *testing.T) {
	lines := map[string]int{"rules": 3, "rules.list": 4, "rules.list[0]": 5, "rules.list[0].match": 6}
	tests := map[string]int{
		"rules.list[0].match":              6,
		"rules.list[0].match.path.regex":   6,
		"rules.list[0].name":               5,
		"rules.list[7].name":               4,
		"rules.default_action":             3,
		"something.else":                   0,
		"":                                 0,
		"rules.list[0].match.header.A.B.C": 6,
	}
	for path, want := range tests {
		if got := nearestLine(lines, path); got != want {
			t.Errorf("nearestLine(%q) = %d, want %d", path, got, want)
		}
	}
}

// The example rule file in the repository is what people copy from. It must
// always load and compile.
func TestExampleRuleFileIsValid(t *testing.T) {
	example, err := os.ReadFile(filepath.Join("..", "..", "examples", "rules", "basic.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	path := writeFiles(t, map[string]string{
		"xibalba.yaml": minimal + "rules:\n  thresholds:\n    - {weight: 10, action: challenge}\n    - {weight: 20, action: deny}\n  files: [basic.yaml]\n",
		"basic.yaml":   string(example),
	})
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("examples/rules/basic.yaml is not valid: %v", err)
	}
	if n := len(cfg.Rules.Spec().Rules); n < 5 {
		t.Errorf("the example has %d rules, expected the full set", n)
	}
}
