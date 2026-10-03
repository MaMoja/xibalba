package config

import (
	"fmt"
	"time"
)

// Trap holds the settings of the hidden link that catches crawlers. It is
// documented in docs/TRAP.md and implemented in internal/trap.
type Trap struct {
	// Enabled hides the link in the pages Xibalba shows and remembers who
	// follows it.
	Enabled bool `yaml:"enabled"`
	// Remember is how long a client that followed the link is remembered.
	Remember time.Duration `yaml:"remember"`
	// Maze answers the link with generated pages of meaningless syllables
	// that link to more such pages, instead of "not found".
	Maze bool `yaml:"maze"`
	// MaxClients is how many clients are remembered at most.
	MaxClients int `yaml:"max_clients"`
}

func defaultTrap() Trap {
	return Trap{Enabled: false, Remember: 24 * time.Hour, Maze: false, MaxClients: 100000}
}

func (t Trap) check(add func(path, message, hint string)) {
	if t.Remember < time.Minute || t.Remember > 30*24*time.Hour {
		add("trap.remember", fmt.Sprintf("%s is out of range", t.Remember), `use a duration from "1m" to "720h"; a day is "24h"`)
	}
	if t.MaxClients < 1000 || t.MaxClients > 5000000 {
		add("trap.max_clients", fmt.Sprintf("%d is out of range", t.MaxClients), "use a number from 1000 to 5000000")
	}
	if t.Maze && !t.Enabled {
		add("trap.maze", "the maze is on but the trap is off", "set trap.enabled to true, or maze to false")
	}
}
