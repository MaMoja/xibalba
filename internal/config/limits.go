package config

import (
	"fmt"
	"net/netip"
	"time"

	"github.com/MaMoja/xibalba/internal/limit"
)

// LimitActions are the allowed values of limits.windows[].action.
var LimitActions = []string{"challenge", "deny"}

// CountByModes are the allowed values of limits.count_by.
var CountByModes = []string{"address", "network"}

// Limits holds the request limits per client. They are documented in
// docs/LIMITS.md and implemented in internal/limit.
type Limits struct {
	// Enabled switches the limits on.
	Enabled bool `yaml:"enabled"`
	// CountBy says what one client is: "address" (an IPv4 address, an IPv6
	// /64) or "network" (an IPv4 /24, an IPv6 /48).
	CountBy string `yaml:"count_by"`
	// Windows are the limits.
	Windows []LimitWindow `yaml:"windows"`
	// Exempt lists addresses and networks that are never counted.
	Exempt []string `yaml:"exempt"`
	// MaxClients is how many clients are tracked at most.
	MaxClients int `yaml:"max_clients"`
}

// LimitWindow is one limit: at most Requests per Per.
type LimitWindow struct {
	// Requests is how many requests a client may send within Per.
	Requests int `yaml:"requests"`
	// Per is the period.
	Per time.Duration `yaml:"per"`
	// Action is what happens to further requests: challenge or deny.
	Action string `yaml:"action"`
}

func defaultLimits() Limits {
	return Limits{
		Enabled:    false,
		CountBy:    "address",
		Windows:    []LimitWindow{{Requests: 300, Per: time.Minute, Action: "challenge"}},
		Exempt:     []string{},
		MaxClients: 100000,
	}
}

// Options returns the limits as options for the limiter. The values were
// validated at load time.
func (l Limits) Options() limit.Options {
	opts := limit.Options{ByNetwork: l.CountBy == "network", MaxClients: l.MaxClients}
	for _, w := range l.Windows {
		opts.Windows = append(opts.Windows, limit.Window{Requests: w.Requests, Per: w.Per, Action: w.Action})
	}
	for _, entry := range l.Exempt {
		if p, err := parsePrefix(entry); err == nil {
			opts.Exempt = append(opts.Exempt, p)
		}
	}
	return opts
}

// check validates the limits. They are checked even when switched off, so a
// mistake does not wait for the day they are switched on.
func (l Limits) check(add func(path, message, hint string)) {
	if !contains(CountByModes, l.CountBy) {
		add("limits.count_by", fmt.Sprintf("%q is not a way to count", l.CountBy),
			"use address (each address on its own) or network (neighbouring addresses together)")
	}
	if len(l.Windows) == 0 || len(l.Windows) > limit.MaxWindows {
		add("limits.windows", fmt.Sprintf("the list has %d entries; it needs 1 to %d", len(l.Windows), limit.MaxWindows),
			`give at least one limit, for example {requests: 300, per: 1m, action: challenge}`)
	}
	seen := map[time.Duration]int{}
	for i, w := range l.Windows {
		field := fmt.Sprintf("limits.windows[%d]", i)
		if w.Requests < 1 || w.Requests > 10000000 {
			add(field+".requests", fmt.Sprintf("%d is out of range", w.Requests), "use a number from 1 to 10000000")
		}
		if w.Per < time.Second || w.Per > 24*time.Hour {
			add(field+".per", fmt.Sprintf("%s is out of range", w.Per), `use a duration from "1s" to "24h"`)
		} else if first, dup := seen[w.Per]; dup {
			add(field+".per", fmt.Sprintf("there is already a limit per %s (entry number %d)", w.Per, first+1), "give every limit its own period")
		}
		seen[w.Per] = i
		if !contains(LimitActions, w.Action) {
			add(field+".action", fmt.Sprintf("%q is not an action a limit can take", w.Action),
				"use challenge (the client has to pass the security check) or deny (further requests are refused)")
		}
	}
	for i, entry := range l.Exempt {
		p, err := parsePrefix(entry)
		if err != nil {
			add(fmt.Sprintf("limits.exempt[%d]", i), fmt.Sprintf("%q is not an IP address or network", entry),
				`use an address such as "192.0.2.7" or a network such as "10.0.0.0/8"`)
			continue
		}
		if p == netip.MustParsePrefix("0.0.0.0/0") || p == netip.MustParsePrefix("::/0") {
			add(fmt.Sprintf("limits.exempt[%d]", i), fmt.Sprintf("%q exempts every address, so nothing would be limited", entry),
				"list the addresses that must not be limited, or set limits.enabled to false")
		}
	}
	if l.MaxClients < 1000 || l.MaxClients > 5000000 {
		add("limits.max_clients", fmt.Sprintf("%d is out of range", l.MaxClients), "use a number from 1000 to 5000000; 100000 needs about 15 MB")
	}
}
