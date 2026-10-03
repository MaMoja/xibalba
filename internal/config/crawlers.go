package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MaMoja/xibalba/data"
	"github.com/MaMoja/xibalba/internal/crawlers"
)

// maxCrawlerFiles limits crawlers.files.
const maxCrawlerFiles = 64

// Crawlers holds the settings for recognising crawlers. What a crawler is
// and how it is verified is documented in docs/CRAWLERS.md and implemented
// in internal/crawlers.
type Crawlers struct {
	// Builtin uses the crawler definitions that ship with Xibalba. Without
	// them only the crawlers from Files are known.
	Builtin bool `yaml:"builtin"`
	// Refresh downloads the address lists that crawler operators publish.
	// Without it, crawlers that are verified by such a list are never
	// counted as genuine.
	Refresh bool `yaml:"refresh"`
	// RefreshInterval is how often the lists are downloaded again.
	RefreshInterval time.Duration `yaml:"refresh_interval"`
	// CacheDir keeps the downloaded lists across restarts. Empty: they are
	// kept in memory only and downloaded again after every start.
	CacheDir string `yaml:"cache_dir"`
	// Files lists additional crawler definition files, relative to the
	// configuration file. A crawler defined there replaces the built-in
	// crawler of the same name.
	Files []string `yaml:"files"`

	// Definitions holds every known crawler: the built-in ones and those
	// from Files. It is filled when the configuration is loaded and is not
	// a setting.
	Definitions []crawlers.Definition `yaml:"-"`
	// CachePath is CacheDir resolved against the configuration file.
	CachePath string `yaml:"-"`
}

func defaultCrawlers() Crawlers {
	return Crawlers{Builtin: true, Refresh: true, RefreshInterval: 24 * time.Hour, CacheDir: "", Files: []string{}}
}

// load reads the built-in definitions and those from Files and checks the
// settings. dir is the directory relative names are resolved against.
func (c *Crawlers) load(dir string, lines map[string]int) []Problem {
	var problems []Problem
	add := func(path, message, hint string) {
		problems = append(problems, Problem{Path: path, Line: nearestLine(lines, path), Message: message, Hint: hint})
	}
	fromFile := func(found []crawlers.Problem) {
		for _, p := range found {
			problems = append(problems, Problem{File: p.File, Path: p.Field, Message: p.Message, Hint: p.Hint})
		}
	}

	if c.RefreshInterval < time.Hour || c.RefreshInterval > 30*24*time.Hour {
		add("crawlers.refresh_interval", fmt.Sprintf("%s is out of range", c.RefreshInterval),
			"use a value from 1h to 720h; operators change their lists rarely, 24h is a good choice")
	}
	if c.CacheDir != "" {
		c.CachePath = c.CacheDir
		if !filepath.IsAbs(c.CachePath) {
			c.CachePath = filepath.Join(dir, c.CachePath)
		}
		info, err := os.Stat(c.CachePath)
		switch {
		case err != nil:
			add("crawlers.cache_dir", fmt.Sprintf("the directory %q cannot be used: it does not exist or cannot be opened", c.CacheDir),
				"create the directory and make it writable for the user Xibalba runs as")
		case !info.IsDir():
			add("crawlers.cache_dir", fmt.Sprintf("%q is not a directory", c.CacheDir), "give a directory")
		}
	}

	var defs []crawlers.Definition
	if c.Builtin {
		found, builtinProblems := crawlers.LoadFS(data.Files, "crawlers")
		fromFile(builtinProblems)
		defs = found
	}
	type place struct{ file, field string }
	places := make([]place, len(defs))
	for i := range defs {
		places[i] = place{file: "built-in crawler definitions", field: defs[i].Name}
	}

	if len(c.Files) > maxCrawlerFiles {
		add("crawlers.files", fmt.Sprintf("%d files are listed; the limit is %d", len(c.Files), maxCrawlerFiles), "combine crawler files")
	}
	seen := map[string]int{}
	for i, name := range c.Files {
		if i >= maxCrawlerFiles {
			break
		}
		field := fmt.Sprintf("crawlers.files[%d]", i)
		if strings.TrimSpace(name) == "" {
			add(field, "the file name is empty", "give the path of a crawler file, or remove the entry")
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
		content, err := readRuleFile(full)
		if err != nil {
			add(field, fmt.Sprintf("the crawler file %q cannot be used: %v", name, err),
				"check the path; it is relative to the directory of the configuration file")
			continue
		}
		found, fileProblems := crawlers.ParseFile(name, content)
		fromFile(fileProblems)
		for j, def := range found {
			// A crawler from a file replaces the built-in one of that name,
			// so a site owner can correct a definition without a new release.
			for k := 0; k < len(defs); k++ {
				if places[k].file == "built-in crawler definitions" && strings.EqualFold(defs[k].Name, def.Name) {
					defs = append(defs[:k], defs[k+1:]...)
					places = append(places[:k], places[k+1:]...)
					k--
				}
			}
			defs = append(defs, def)
			places = append(places, place{file: name, field: fmt.Sprintf("crawlers[%d]", j)})
		}
	}
	fromFile(crawlers.CheckUnique(defs, func(i int) (string, string) { return places[i].file, places[i].field }))

	c.Definitions = defs
	return problems
}
