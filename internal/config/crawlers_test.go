package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MaMoja/xibalba/internal/rules"
)

const base = "upstream:\n  url: http://127.0.0.1:3000\n"

func TestCrawlerDefaults(t *testing.T) {
	cfg, err := Parse("xibalba.yaml", []byte(base))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Crawlers.Definitions) < 20 || cfg.Rules.Catalog == nil || len(cfg.Rules.Catalog.Names) != len(cfg.Crawlers.Definitions) {
		t.Fatalf("definitions: %d, catalog: %+v", len(cfg.Crawlers.Definitions), cfg.Rules.Catalog)
	}
	engine, problems := rules.Compile(cfg.Rules.Spec())
	if len(problems) > 0 || engine.UsesCrawlers() {
		t.Errorf("default rule set: problems %+v, uses crawlers %v", problems, engine.UsesCrawlers())
	}
}

// Every preset must be valid, alone and all together, in any listed order.
func TestPresets(t *testing.T) {
	names := PresetNames()
	if len(names) < 6 {
		t.Fatalf("presets: %v", names)
	}
	for _, name := range names {
		cfg, err := Parse("xibalba.yaml", []byte(base+"rules:\n  presets: ["+name+"]\n"))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		spec := cfg.Rules.Spec()
		if len(spec.Rules) == 0 {
			t.Errorf("%s has no rules", name)
		}
		for _, r := range spec.Rules {
			if r.Name != "preset."+name && !strings.HasPrefix(r.Name, "preset."+name+".") {
				t.Errorf("%s: rule %q is not named after its preset", name, r.Name)
			}
		}
	}
	cfg, err := Parse("xibalba.yaml", []byte(base+"rules:\n  presets: ["+strings.Join(names, ", ")+"]\n"))
	if err != nil {
		t.Fatalf("all presets together: %v", err)
	}
	if engine, _ := rules.Compile(cfg.Rules.Spec()); engine == nil || !engine.UsesCrawlers() {
		t.Error("the presets do not use crawler conditions")
	}
}

func TestPresetsComeAfterTheListAndBeforeFiles(t *testing.T) {
	path := writeFiles(t, map[string]string{
		"xibalba.yaml": base + `rules:
  presets: [block-ai-training, allow-search-engines]
  files: [more.yaml]
  list:
    - {name: mine, match: {path: {prefix: /x}}, action: deny}
`,
		"more.yaml": "rules:\n  - {name: theirs, match: {path: {prefix: /y}}, action: deny}\n",
	})
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := ruleNames(cfg.Rules.Spec()); got != "mine preset.block-ai-training preset.allow-search-engines theirs" {
		t.Errorf("order = %s", got)
	}
}

func TestCrawlerSettingProblems(t *testing.T) {
	tests := []struct{ name, yaml, path, message string }{
		{"unknown preset", "rules:\n  presets: [block-everything]\n", "rules.presets[0]", "is not a preset"},
		{"preset twice", "rules:\n  presets: [allow-ai-search, allow-ai-search]\n", "rules.presets[1]", "already listed"},
		{"interval too short", "crawlers:\n  refresh_interval: 1m\n", "crawlers.refresh_interval", "out of range"},
		{"cache dir missing", "crawlers:\n  cache_dir: nowhere\n", "crawlers.cache_dir", "cannot be used"},
		{"file missing", "crawlers:\n  files: [none.yaml]\n", "crawlers.files[0]", "does not exist"},
		{"unknown crawler name in a rule", "rules:\n  list:\n    - name: r\n      match:\n        crawler: {name: [NoSuchBot]}\n      action: deny\n",
			"rules.list[0].match.crawler.name[0]", "not a known crawler name"},
		{"allow by name", "rules:\n  list:\n    - name: r\n      match:\n        crawler: {name: [Googlebot]}\n      action: allow\n",
			"rules.list[0].match.crawler.verified", "must make sure it is genuine"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFiles(t, map[string]string{"xibalba.yaml": base + tt.yaml})
			_, err := Load(path)
			if err == nil {
				t.Fatal("no error")
			}
			text := err.Error()
			if !strings.Contains(text, tt.path) || !strings.Contains(text, tt.message) || !strings.Contains(text, "line ") {
				t.Errorf("error does not name %q, %q and a line:\n%s", tt.path, tt.message, text)
			}
		})
	}
}

const ownCrawlers = `operator: City of Example
source: https://example.org/monitoring
checked: 2026-10-03
crawlers:
  - name: CityMonitor
    class: other
    user_agent: CityMonitor
    purpose: Checks that the website is reachable.
    verify:
      ranges: ["192.0.2.10"]
`

func TestOwnCrawlerFiles(t *testing.T) {
	path := writeFiles(t, map[string]string{
		"xibalba.yaml": base + `crawlers:
  cache_dir: cache
  files: [crawlers/own.yaml, crawlers/gptbot.yaml]
rules:
  list:
    - name: monitoring
      match:
        crawler: {name: [CityMonitor], verified: true}
      action: allow
`,
		"cache/.keep":       "",
		"crawlers/own.yaml": ownCrawlers,
		// Replaces the built-in GPTBot.
		"crawlers/gptbot.yaml": strings.NewReplacer("CityMonitor", "GPTBot", "class: other", "class: archive").Replace(ownCrawlers),
	})
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Crawlers.CachePath != filepath.Join(filepath.Dir(path), "cache") {
		t.Errorf("CachePath = %q", cfg.Crawlers.CachePath)
	}
	seen := map[string]string{}
	for _, d := range cfg.Crawlers.Definitions {
		if _, dup := seen[d.Name]; dup {
			t.Errorf("%s is defined twice", d.Name)
		}
		seen[d.Name] = string(d.Class)
	}
	if seen["CityMonitor"] != "other" || seen["GPTBot"] != "archive" {
		t.Errorf("CityMonitor: %q, GPTBot: %q", seen["CityMonitor"], seen["GPTBot"])
	}
}

func TestProblemsInCrawlerFilesNameTheFile(t *testing.T) {
	tests := []struct{ name, content, message string }{
		{"bad class", strings.Replace(ownCrawlers, "class: other", "class: friendly", 1), "crawlers[0].class"},
		{"same user agent as a built-in crawler", strings.Replace(ownCrawlers, "user_agent: CityMonitor", "user_agent: gptbot", 1), "already used by the crawler GPTBot"},
		{"not YAML", "operator: [", "not valid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFiles(t, map[string]string{"xibalba.yaml": base + "crawlers:\n  files: [own.yaml]\n", "own.yaml": tt.content})
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), "own.yaml") || !strings.Contains(err.Error(), tt.message) {
				t.Errorf("error = %v", err)
			}
		})
	}
}

func TestBuiltInCrawlersCanBeTurnedOff(t *testing.T) {
	path := writeFiles(t, map[string]string{
		"xibalba.yaml": base + "crawlers:\n  builtin: false\n  files: [own.yaml]\n", "own.yaml": ownCrawlers,
	})
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Crawlers.Definitions) != 1 || cfg.Crawlers.Definitions[0].Name != "CityMonitor" {
		t.Errorf("definitions = %+v", cfg.Crawlers.Definitions)
	}
	// A rule that names a built-in crawler is then a mistake, and says so.
	_, err = Parse("xibalba.yaml", []byte(base+"crawlers:\n  builtin: false\nrules:\n  list:\n    - {name: r, match: {crawler: {name: [GPTBot]}}, action: deny}\n"))
	if err == nil || !strings.Contains(err.Error(), "not a known crawler name") {
		t.Errorf("error = %v", err)
	}
}

// The preset files are also the examples in the documentation.
func TestPresetFilesHaveAComment(t *testing.T) {
	for _, name := range PresetNames() {
		content, err := os.ReadFile(filepath.Join("..", "..", "data", "presets", name+".yaml"))
		if err != nil || !strings.HasPrefix(string(content), "# ") {
			t.Errorf("%s: no leading comment (%v)", name, err)
		}
	}
}
