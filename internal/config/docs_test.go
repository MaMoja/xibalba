package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var yamlBlock = regexp.MustCompile("(?s)```yaml\n(.*?)```")

// ruleFileRE matches a document whose "rules" key directly holds a list.
var ruleFileRE = regexp.MustCompile(`(?m)^rules:\n(?:\s*#.*\n|\s*\n)*\s+- name:`)

// Every YAML example in the documentation must be something Xibalba accepts.
// The examples are what operators copy; a typo in one is a broken set-up.
//
// An example is one of four shapes, recognised by how it starts:
//   - a rule or several rules ("- name: ..."), as they appear in rules.list;
//   - the conditions of a rule ("match: ...");
//   - a rule file ("rules:" followed directly by a list of rules);
//   - a fragment of the configuration file (anything else).
func TestYAMLExamplesInTheDocumentationAreValid(t *testing.T) {
	root := filepath.Join("..", "..")
	var docs []string
	for _, pattern := range []string{"README.md", "docs/*.md", "docs/de/*.md"} {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, matches...)
	}
	if len(docs) < 5 {
		t.Fatalf("found only %d documents; the search is broken", len(docs))
	}

	checked := 0
	for _, doc := range docs {
		data, err := os.ReadFile(doc)
		if err != nil {
			t.Fatal(err)
		}
		name, _ := filepath.Rel(root, doc)
		for i, m := range yamlBlock.FindAllStringSubmatch(string(data), -1) {
			example := m[1]
			checked++

			dir := t.TempDir()
			// Paths in examples point at places that exist on a server, not here.
			example = strings.ReplaceAll(example, "/var/lib/xibalba/xibalba.key", "xibalba.key")
			files := map[string]string{}
			for _, ref := range regexp.MustCompile(`(?m)^\s+- ((?:rules|examples)/[\w./-]+\.yaml)`).FindAllStringSubmatch(example, -1) {
				files[ref[1]] = "rules: []\n"
			}

			// Recognise the shape by the first line that is not a comment.
			first := ""
			for _, line := range strings.Split(example, "\n") {
				if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "#") {
					first = line
					break
				}
			}
			const thresholds = "rules:\n  thresholds:\n    - {weight: 10, action: challenge}\n"
			var config string
			switch {
			case strings.HasPrefix(first, "- name:"):
				config = minimal + thresholds + "  list:\n" + indent(example, "    ")
			case strings.HasPrefix(first, "match:"):
				config = minimal + thresholds + "  list:\n    - name: example\n" + indent(example, "      ") + "      action: deny\n"
			case first == "rules:" && ruleFileRE.MatchString(example):
				files["example-rules.yaml"] = example
				config = minimal + thresholds + "  files: [example-rules.yaml]\n"
			default:
				config = example
				if !regexp.MustCompile(`(?m)^upstream:`).MatchString(example) {
					config += minimal
				}
			}

			for file, content := range files {
				path := filepath.Join(dir, file)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(dir, "xibalba.yaml")
			if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err != nil {
				t.Errorf("%s, YAML example %d is not valid:\n%v\n--- example ---\n%s", name, i+1, err, m[1])
			}
		}
	}
	if checked < 20 {
		t.Errorf("checked only %d examples; expected the documentation to hold more", checked)
	}
}

func indent(text, prefix string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "" {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// The handbook shows the nginx and Caddy configurations that are shipped in
// examples/ and tested by test/webserver/check.py. Every line it shows must
// be a line of the tested file, so the handbook cannot drift from what works.
func TestWebServerExamplesInTheHandbookMatchTheTestedFiles(t *testing.T) {
	root := filepath.Join("..", "..")
	handbook, err := os.ReadFile(filepath.Join(root, "docs", "de", "HANDBUCH.md"))
	if err != nil {
		t.Fatal(err)
	}
	for language, file := range map[string]string{
		"nginx": filepath.Join("examples", "nginx", "xibalba.conf"),
		"caddy": filepath.Join("examples", "caddy", "Caddyfile"),
	} {
		tested, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		lines := map[string]bool{}
		for _, line := range strings.Split(string(tested), "\n") {
			lines[strings.Join(strings.Fields(line), " ")] = true
		}
		blocks := regexp.MustCompile("(?s)```"+language+"\n(.*?)```").FindAllStringSubmatch(string(handbook), -1)
		if len(blocks) == 0 {
			t.Errorf("the handbook shows no %s example", language)
		}
		for _, block := range blocks {
			for _, line := range strings.Split(block[1], "\n") {
				normal := strings.Join(strings.Fields(line), " ")
				if normal == "" || strings.HasPrefix(normal, "#") {
					continue
				}
				if !lines[normal] {
					t.Errorf("the handbook's %s example has the line %q, which is not in the tested file %s", language, normal, file)
				}
			}
		}
	}
}
