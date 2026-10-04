// Package rules decides what happens to a request: allow, deny or challenge.
//
// A rule set is compiled once, at start-up, into an Engine. Compiling checks
// every rule and reports all mistakes; after that, Evaluate cannot fail. The
// engine is immutable and safe for concurrent use, performs no I/O, and does
// not allocate while evaluating.
//
// The package knows nothing about HTTP servers or configuration files. It
// takes a description of a request and returns a Decision. Enforcing the
// decision is the job of internal/gate.
//
// # How a request is evaluated
//
// Rules are checked from top to bottom. A rule whose conditions all hold
// "matches". What happens next depends on its action:
//
//   - allow, deny, challenge: the decision is made and evaluation stops.
//   - weigh: the rule's weight is added to the request's score and evaluation
//     continues with the next rule.
//
// If no rule decides, the score is compared with the thresholds, highest
// first; the first threshold the score reaches decides. If none is reached,
// the default action applies.
//
// # What can be trusted
//
// Only the client address (the ip condition) is established by Xibalba
// itself. Everything else in a request, including the user agent and every
// header, is written by the client and can be forged. A rule that allows
// traffic should therefore never rest on a header alone.
package rules

import (
	"net/http"
	"net/netip"
)

// Action is what a rule, a threshold or the default does to a request.
type Action string

const (
	// Allow sends the request to the website.
	Allow Action = "allow"
	// Deny refuses the request.
	Deny Action = "deny"
	// Challenge asks the client to prove it is a browser before going on.
	Challenge Action = "challenge"
	// Weigh adds the rule's weight to the request's score and continues.
	// Only rules can weigh.
	Weigh Action = "weigh"
)

// Limits on a rule set. They keep evaluation cost bounded and predictable.
const (
	// MaxRules is the largest number of rules in one rule set.
	MaxRules = 10000
	// MaxDepth is how deeply all/any/not may be nested.
	MaxDepth = 8
	// MaxConditions is the largest number of entries in one all, any, ip,
	// method or header list.
	MaxConditions = 1024
	// MaxPatternLength is the longest text or regular expression in a condition.
	MaxPatternLength = 512
	// MaxWeight is the largest weight, positive or negative, of a rule or threshold.
	MaxWeight = 1000
)

// Spec describes a rule set before it is compiled.
type Spec struct {
	// DefaultAction applies when no rule decides and no threshold is reached.
	// It is allow, deny or challenge.
	DefaultAction Action
	// Thresholds turn a request's score into an action.
	Thresholds []ThresholdSpec
	// Rules are evaluated in order.
	Rules []RuleSpec
	// Crawlers lists the crawler classes and names that crawler conditions
	// may refer to. Without it a crawler condition is a mistake.
	Crawlers *Catalog
	// Countries says that a country database is configured. Without it a
	// country condition is a mistake, because it could never hold.
	Countries bool
	// Trap says that the trap is switched on. Without it a trapped
	// condition is a mistake, because it could never hold.
	Trap bool
}

// Catalog lists what crawler conditions may refer to.
type Catalog struct {
	// Classes are the crawler classes, such as "training".
	Classes []string
	// Names are the crawler names, such as "GPTBot".
	Names []string
}

// ThresholdSpec says: at this score or above, take this action.
type ThresholdSpec struct {
	// Weight is the score at which the threshold is reached. At least 1.
	Weight int `yaml:"weight"`
	// Action is challenge or deny.
	Action Action `yaml:"action"`
}

// RuleSpec is one rule.
type RuleSpec struct {
	// Name identifies the rule in statistics and on the block page reference.
	// Lower-case letters, digits, dot, underscore and hyphen; unique in the set.
	Name string `yaml:"name"`
	// Match holds the conditions. All of them must hold for the rule to match.
	Match MatchSpec `yaml:"match"`
	// Action is allow, deny, challenge or weigh.
	Action Action `yaml:"action"`
	// Weight is added to the score when Action is weigh. It may be negative.
	// It must be left out for every other action.
	Weight int `yaml:"weight"`
	// ExemptFromLimits, on an allow rule, says that requests the rule lets
	// through are not counted by the request limits. Use it only where the
	// client cannot choose to match: an address, a verified crawler.
	ExemptFromLimits bool `yaml:"exempt_from_limits"`
}

// MatchSpec is a set of conditions that must all hold.
type MatchSpec struct {
	// Method lists HTTP methods; the request's method must be one of them.
	Method []string `yaml:"method"`
	// Host tests the host name the client asked for, without the port.
	// Host names are compared without regard to case.
	Host *StringSpec `yaml:"host"`
	// Path tests the request path after normalisation (see NormalizePath).
	Path *StringSpec `yaml:"path"`
	// Query tests the query of the address: what follows the "?", as the
	// client sent it. {present: false} holds if there is none.
	Query *StringSpec `yaml:"query"`
	// UserAgent tests the User-Agent header.
	UserAgent *StringSpec `yaml:"user_agent"`
	// Header tests other headers by name. A header with several values
	// matches if any value does.
	Header map[string]*StringSpec `yaml:"header"`
	// IP lists addresses and networks; the client address must be in one of them.
	IP []string `yaml:"ip"`
	// Crawler tests which known crawler the request claims to be and whether
	// that claim was verified.
	Crawler *CrawlerSpec `yaml:"crawler"`
	// Country lists country codes (ISO 3166-1, two letters); the client's
	// address must be registered in one of them. An address whose country
	// is not known is in none.
	Country []string `yaml:"country"`
	// Trapped tests whether the client recently followed the hidden trap
	// link (true) or did not (false).
	Trapped *bool `yaml:"trapped"`
	// All holds groups of conditions that must all hold.
	All []MatchSpec `yaml:"all"`
	// Any holds groups of conditions of which at least one must hold.
	Any []MatchSpec `yaml:"any"`
	// Not holds conditions that must not hold.
	Not *MatchSpec `yaml:"not"`
}

// CrawlerSpec tests the crawler a request claims to be. A request claims to
// be a crawler by carrying its name in the user agent. At least one of Class,
// Name and Verified must be set; those that are set must all hold.
type CrawlerSpec struct {
	// Class lists crawler classes; the crawler must be of one of them.
	Class []string `yaml:"class"`
	// Name lists crawler names; the crawler must be one of them.
	Name []string `yaml:"name"`
	// Verified, if true, holds only for requests that really come from the
	// crawler's operator. If false, it holds only for requests that were
	// checked and do not: impostors. Left out, the claim alone is enough.
	// A crawler that cannot be verified, or is not verified yet, is neither.
	Verified *bool `yaml:"verified"`
}

// CrawlerStatus says what is known about a request's claim to be a crawler.
type CrawlerStatus uint8

const (
	// CrawlerNone: the request does not claim to be a known crawler.
	CrawlerNone CrawlerStatus = iota
	// CrawlerVerified: the request comes from the crawler's operator.
	CrawlerVerified
	// CrawlerImpostor: the request carries the name but was checked and
	// does not come from the crawler's operator.
	CrawlerImpostor
	// CrawlerUnknown: the claim cannot be checked, or is not checked yet.
	CrawlerUnknown
)

// Crawler is the crawler a request claims to be.
type Crawler struct {
	// Name and Class describe the crawler. Empty if Status is CrawlerNone.
	Name  string
	Class string
	// Status says whether the claim is true.
	Status CrawlerStatus
}

// StringSpec tests a piece of text. Exactly one of Equals, Contains, Prefix,
// Suffix, Regex or (for headers only) Present must be set.
type StringSpec struct {
	// Equals holds if the text is exactly this.
	Equals string `yaml:"equals"`
	// Contains holds if the text contains this.
	Contains string `yaml:"contains"`
	// Prefix holds if the text starts with this.
	Prefix string `yaml:"prefix"`
	// Suffix holds if the text ends with this.
	Suffix string `yaml:"suffix"`
	// Regex holds if the regular expression (RE2 syntax) matches anywhere in the text.
	Regex string `yaml:"regex"`
	// Present, for headers only, holds if the header is there (true) or not there (false).
	Present *bool `yaml:"present"`
	// CaseSensitive makes the comparison distinguish upper and lower case.
	// By default it does not, because a rule that misses "gptbot" when it
	// says "GPTBot" fails silently.
	CaseSensitive bool `yaml:"case_sensitive"`
}

// Request is what the engine needs to know about a request.
type Request struct {
	// Method is the HTTP method in upper case.
	Method string
	// Host is the host name in lower case without a port (see NormalizeHost).
	Host string
	// Path is the normalised path (see NormalizePath).
	Path string
	// Query is the query of the address as the client sent it, without the "?".
	Query string
	// UserAgent is the User-Agent header.
	UserAgent string
	// Header holds the request headers with canonical names.
	Header http.Header
	// Client is the client address resolved by internal/clientip. If it is
	// not valid, no ip condition matches.
	Client netip.Addr
	// Country is the country the client's address is registered in, as
	// two upper-case letters, established by internal/geo. The zero value
	// means: not known.
	Country [2]byte
	// PathAltered reports that Path is not what the client sent: the
	// address was written in a roundabout way (dot segments, repeated
	// slashes, backslashes, path parameters, encoded slashes). Rules that
	// let a request through because of its path are then skipped. The
	// website receives the address as sent, and may read a roundabout
	// address differently than the rule did; a rule that restricts is
	// safe to err on the wide side, a rule that lets through is not.
	PathAltered bool
	// NoCountryData reports that no country database is loaded right now.
	// Rules with a country condition are then skipped altogether: without
	// data, "in Germany" and "not in Germany" are equally unknown, and a
	// rule such as "deny everyone outside Germany" must not shut out
	// everybody because a file is missing.
	NoCountryData bool
	// Trapped reports that the client recently followed the hidden trap
	// link, established by internal/trap.
	Trapped bool
	// Crawler is the crawler the request claims to be, established by
	// internal/crawlers. The zero value means: none.
	Crawler Crawler
}

// Decision is the outcome of evaluating one request.
type Decision struct {
	// Action is allow, deny or challenge. It is never weigh.
	Action Action
	// Source is the index into Engine.Sources of what made the decision.
	Source int
	// Weight is the request's score when the decision was made.
	Weight int
}

// Source is something that can decide: a rule, a threshold, or the default.
type Source struct {
	// ID is "rule:<name>", "threshold:<weight>" or "default".
	ID string
	// ExemptFromLimits reports that requests this source lets through are
	// not counted by the request limits.
	ExemptFromLimits bool
	// Action is what this source decides.
	Action Action
	// Reference is a short, stable code for this source. It is shown to a
	// blocked visitor so the site owner can find the responsible rule
	// without Xibalba having to record who was blocked.
	Reference string
}

// Problem is one mistake in a Spec.
type Problem struct {
	// Rule is the index of the rule the problem is in, or -1 if it concerns
	// the default action or a threshold.
	Rule int
	// Field is the path of the offending setting: relative to the rule, such
	// as "match.user_agent.regex", or for Rule -1 a path such as
	// "default_action" or "thresholds[1].weight".
	Field string
	// Message says what is wrong.
	Message string
	// Hint says how to fix it.
	Hint string
}
