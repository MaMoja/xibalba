// Package data holds the data files that are built into Xibalba: the
// definitions of known crawlers and the ready-made rule sets (presets).
//
// They are data, not code. Every crawler definition names the page of the
// crawler's operator it was taken from and the day it was checked.
package data

import "embed"

// Files holds data/crawlers/*.yaml and data/presets/*.yaml.
//
//go:embed crawlers/*.yaml presets/*.yaml
var Files embed.FS
