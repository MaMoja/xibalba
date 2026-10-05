package config

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/MaMoja/xibalba/internal/preview"
)

// Previews holds the settings of the link previews: the title, description
// and picture of a protected page, shown on the challenge page for the
// services that build a preview of a shared link. It is documented in
// docs/PREVIEWS.md and implemented in internal/preview.
type Previews struct {
	// Enabled puts the tags on the challenge page.
	Enabled bool `yaml:"enabled"`
	// TTL is how long the tags of a page are kept before they are fetched
	// from the website again.
	TTL time.Duration `yaml:"ttl"`
	// MaxPages is how many pages are remembered at most.
	MaxPages int `yaml:"max_pages"`
	// FetchPerMinute is how many pages are fetched from the website per
	// minute at most.
	FetchPerMinute int `yaml:"fetch_per_minute"`
	// Query treats addresses that differ after the "?" as different pages.
	Query bool `yaml:"query"`
	// SkipPaths lists beginnings of paths whose pages are never fetched
	// and get no tags.
	SkipPaths []string `yaml:"skip_paths"`
	// Tags, if not empty, are used for every page; nothing is fetched.
	Tags map[string]string `yaml:"tags"`
}

func defaultPreviews() Previews {
	return Previews{Enabled: false, TTL: 24 * time.Hour, MaxPages: 1000, FetchPerMinute: 30, Query: false, SkipPaths: []string{}, Tags: map[string]string{}}
}

func (p Previews) check(add func(path, message, hint string)) {
	if p.TTL < time.Minute || p.TTL > 30*24*time.Hour {
		add("previews.ttl", fmt.Sprintf("%s is out of range", p.TTL), `use a duration from "1m" to "720h"; a day is "24h"`)
	}
	if p.MaxPages < 1 || p.MaxPages > 20000 {
		add("previews.max_pages", fmt.Sprintf("%d is out of range", p.MaxPages), "use a number from 1 to 20000; 1000 pages need 6 MB at the very most, usually under 1 MB")
	}
	if len(p.SkipPaths) > 100 {
		add("previews.skip_paths", fmt.Sprintf("%d entries are too many", len(p.SkipPaths)), "give 100 at most")
	}
	for i, prefix := range p.SkipPaths {
		if !strings.HasPrefix(prefix, "/") || len(prefix) > 512 {
			add(fmt.Sprintf("previews.skip_paths[%d]", i), fmt.Sprintf("%q is not the beginning of a path", prefix), `start it with "/", for example "/intern/"`)
		}
	}
	total := 0
	for key, value := range p.Tags {
		total += len(key) + len(value)
	}
	if total > preview.MaxBytes {
		add("previews.tags", fmt.Sprintf("the tags take %d bytes together", total), fmt.Sprintf("shorten them to %d bytes", preview.MaxBytes))
	}
	if p.FetchPerMinute < 1 || p.FetchPerMinute > 600 {
		add("previews.fetch_per_minute", fmt.Sprintf("%d is out of range", p.FetchPerMinute), "use a number from 1 to 600")
	}
	if len(p.Tags) > preview.MaxTags {
		add("previews.tags", fmt.Sprintf("%d tags are too many", len(p.Tags)), fmt.Sprintf("give %d at most", preview.MaxTags))
	}
	keys := make([]string, 0, len(p.Tags))
	for key := range p.Tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if msg := preview.CheckTag(key, p.Tags[key]); msg != "" {
			add("previews.tags."+key, "this tag cannot be used", msg)
		}
	}
}
