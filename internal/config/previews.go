package config

import (
	"fmt"
	"sort"
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
	// Tags, if not empty, are used for every page; nothing is fetched.
	Tags map[string]string `yaml:"tags"`
}

func defaultPreviews() Previews {
	return Previews{Enabled: false, TTL: 24 * time.Hour, MaxPages: 1000, FetchPerMinute: 30, Query: false, Tags: map[string]string{}}
}

func (p Previews) check(add func(path, message, hint string)) {
	if p.TTL < time.Minute || p.TTL > 30*24*time.Hour {
		add("previews.ttl", fmt.Sprintf("%s is out of range", p.TTL), `use a duration from "1m" to "720h"; a day is "24h"`)
	}
	if p.MaxPages < 1 || p.MaxPages > 100000 {
		add("previews.max_pages", fmt.Sprintf("%d is out of range", p.MaxPages), "use a number from 1 to 100000; 1000 pages need about 2 MB at most")
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
