// Package metrics serves Xibalba's numbers in the Prometheus text format, so
// that a monitoring system can collect them.
//
// The package knows nothing about what is measured. The parts of Xibalba
// hand it functions that write their current numbers; main does the wiring.
// What is served is exactly what the JSON endpoints show: counts and states,
// never an address, a path or a user agent.
package metrics

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// Writer collects samples for one answer.
type Writer struct {
	buf      bytes.Buffer
	declared map[string]bool
}

// Counter writes one sample of a value that only ever grows. labels are
// pairs of name and value. All samples of one metric must be written one
// after the other.
func (w *Writer) Counter(name, help string, value float64, labels ...string) {
	w.sample(name, "counter", help, value, labels)
}

// Gauge writes one sample of a value that can go up and down.
func (w *Writer) Gauge(name, help string, value float64, labels ...string) {
	w.sample(name, "gauge", help, value, labels)
}

func (w *Writer) sample(name, kind, help string, value float64, labels []string) {
	if !validName(name) {
		return
	}
	if !w.declared[name] {
		w.declared[name] = true
		w.buf.WriteString("# HELP " + name + " " + escape(help, false) + "\n")
		w.buf.WriteString("# TYPE " + name + " " + kind + "\n")
	}
	w.buf.WriteString(name)
	wrote := false
	for i := 0; i+1 < len(labels); i += 2 {
		if !validName(labels[i]) {
			continue
		}
		if wrote {
			w.buf.WriteByte(',')
		} else {
			w.buf.WriteByte('{')
			wrote = true
		}
		w.buf.WriteString(labels[i] + `="` + escape(labels[i+1], true) + `"`)
	}
	if wrote {
		w.buf.WriteByte('}')
	}
	w.buf.WriteByte(' ')
	w.buf.WriteString(strconv.FormatFloat(value, 'g', -1, 64))
	w.buf.WriteByte('\n')
}

// validName reports whether s can be a metric or label name.
func validName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		b := s[i]
		letter := b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
		if !letter && (i == 0 || b < '0' || b > '9') {
			return false
		}
	}
	return true
}

// escape makes a text safe inside a help line or, with quotes, a label value.
func escape(s string, quotes bool) string {
	if !strings.ContainsAny(s, "\\\n\"") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\':
			b.WriteString(`\\`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '"' && quotes:
			b.WriteString(`\"`)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Registry holds the functions that write the numbers.
type Registry struct {
	mu         sync.Mutex
	collectors []func(*Writer)
}

// New returns an empty Registry.
func New() *Registry { return &Registry{} }

// Add registers a function that writes its current numbers when asked.
func (r *Registry) Add(collect func(*Writer)) {
	r.mu.Lock()
	r.collectors = append(r.collectors, collect)
	r.mu.Unlock()
}

// Render returns the current numbers in the text format. A collector that
// panics loses its numbers; the others are still served.
func (r *Registry) Render() []byte {
	r.mu.Lock()
	collectors := make([]func(*Writer), len(r.collectors))
	copy(collectors, r.collectors)
	r.mu.Unlock()

	w := &Writer{declared: map[string]bool{}}
	for _, collect := range collectors {
		func() {
			mark := w.buf.Len()
			defer func() {
				if recover() != nil {
					w.buf.Truncate(mark) // no half-written metric
				}
			}()
			collect(w)
		}()
	}
	return w.buf.Bytes()
}

// Handler serves the numbers for the operations listener.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(r.Render())
	})
}
