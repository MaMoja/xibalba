// Package stats keeps Xibalba's counters by the hour, on disk, so that they
// survive a restart and can be looked at over weeks and months.
//
// It knows nothing about what is counted. It is handed a function that
// returns the running totals under names; every minute it works out what was
// added since the last look and puts that into the current hour. A finished
// hour is appended as one line to that month's file. Old months are removed.
//
// What is stored are counts under the names of rules, crawlers and limits.
// No address, path or user agent comes near this package.
//
// The package is off the request path: if the disk is full or the directory
// gone, health says so and requests are served as before.
package stats

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MaMoja/xibalba/internal/health"
)

// Limits.
const (
	// MaxKeepDays is the longest time statistics can be kept.
	MaxKeepDays = 3650
	// maxLine is the longest line read from a month's file.
	maxLine = 4 << 20
	// reduceAt is the number of names from which an hour is reduced before
	// it is over, if there is a Reduce.
	reduceAt = 5000
	// maxNames is how many different names one hour may hold.
	maxNames = 20000
)

// Options configures a Store.
type Options struct {
	// Name is the component's name; empty means "statistics".
	Name string
	// Dir is the directory the files are kept in. It must exist, unless
	// Create is set.
	Dir string
	// Create makes Dir at the start if it is missing. Its parent must exist.
	Create bool
	// KeepDays is how long an hour is kept.
	KeepDays int
	// Collect returns the running totals by name. Totals only grow while
	// Xibalba runs and start at zero with every start.
	Collect func() map[string]uint64
	// Added returns what was counted since it was last asked, by name, and
	// is an alternative to Collect for a source that forgets what it hands
	// over. Either may be nil.
	Added func() map[string]uint64
	// Reduce, if set, is applied to an hour's counts before the hour is
	// written to its month's file, for example to keep only the largest.
	Reduce func(counts map[string]uint64)
	// Every is how often Collect is asked. Zero means a minute.
	Every time.Duration
	// Log receives the store's messages.
	Log *slog.Logger
	// Now returns the current time. Tests replace it; nil means time.Now.
	Now func() time.Time
}

// Bucket holds what was counted in one hour.
type Bucket struct {
	// Hour is the start of the hour, in UTC.
	Hour time.Time `json:"hour"`
	// Counts holds what was added in that hour, by name. Names with
	// nothing added are left out.
	Counts map[string]uint64 `json:"counts"`
}

// Store keeps the counters. It is a lifecycle component.
type Store struct {
	opts Options
	log  *slog.Logger

	mu      sync.Mutex
	last    map[string]uint64 // totals at the last look
	current Bucket            // the hour being filled
	problem string            // why the last write failed, or ""
	cleaned time.Time         // when old files were last removed

	cancel context.CancelFunc
	done   chan struct{}
}

// New returns a Store. It reads and writes nothing until Start.
func New(opts Options) *Store {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Every <= 0 {
		opts.Every = time.Minute
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Name == "" {
		opts.Name = "statistics"
	}
	return &Store{opts: opts, log: opts.Log.With("component", opts.Name), last: map[string]uint64{}}
}

// Name implements lifecycle.Component.
func (s *Store) Name() string { return s.opts.Name }

func hourOf(t time.Time) time.Time { return t.UTC().Truncate(time.Hour) }

func (s *Store) checkpointPath() string { return filepath.Join(s.opts.Dir, "current.json") }

func (s *Store) monthPath(hour time.Time) string {
	return filepath.Join(s.opts.Dir, "hours-"+hour.UTC().Format("2006-01")+".jsonl")
}

// Start takes up the hour that was being filled when Xibalba last stopped
// and begins the regular looks. A directory that cannot be used does not
// keep Xibalba from starting; health reports it.
func (s *Store) Start(context.Context) error {
	s.mu.Lock()
	if s.opts.Create {
		_ = os.Mkdir(s.opts.Dir, 0o700) // fails if it exists; a real problem shows at the first write
	}
	s.current = Bucket{Hour: hourOf(s.opts.Now()), Counts: map[string]uint64{}}
	if kept, err := readCheckpoint(s.checkpointPath()); err != nil {
		s.log.Warn("the hour that was being counted at the last stop could not be read and is lost", "error", err.Error())
	} else if kept != nil {
		if kept.Hour.Equal(s.current.Hour) {
			s.current = *kept // same hour: carry on
		} else if err := s.appendHour(*kept); err != nil { // an earlier hour: it is finished
			s.fail(err)
		}
	}
	// What was counted before this start is not ours to add again.
	s.last = map[string]uint64{}
	s.clean()
	s.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(s.opts.Every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.Sample()
			}
		}
	}()
	return nil
}

// Stop takes a last look and writes the current hour down, so nothing
// counted is lost by a restart.
func (s *Store) Stop(ctx context.Context) error {
	if s.cancel == nil {
		return nil
	}
	s.cancel()
	select {
	case <-s.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.Sample()
	return nil
}

// Sample asks for the totals, adds what is new to the current hour, moves
// on to the next hour if the clock has, and writes the state down.
func (s *Store) Sample() {
	totals, added := s.collect()
	now := s.opts.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	if hour := hourOf(now); !hour.Equal(s.current.Hour) {
		// The hour is over. What is new since the last look is put into
		// the new hour; it is at most one interval's worth.
		if err := s.appendHour(s.current); err != nil {
			s.fail(err)
		} else {
			s.current = Bucket{Hour: hour, Counts: map[string]uint64{}}
		}
		if hour.Sub(s.cleaned) >= 24*time.Hour {
			s.clean()
		}
	}
	for name, total := range totals {
		added := total - s.last[name]
		if total < s.last[name] { // the counter started again
			added = total
		}
		if added > 0 && (len(s.current.Counts) < maxNames || s.current.Counts[name] > 0) {
			s.current.Counts[name] += added
		}
	}
	s.last = totals
	for name, n := range added {
		if n > 0 && (len(s.current.Counts) < maxNames || s.current.Counts[name] > 0) {
			s.current.Counts[name] += n
		}
	}
	if s.opts.Reduce != nil && len(s.current.Counts) > reduceAt {
		// Too many names for one hour, such as requests from thousands of
		// networks: reduce early, so the hour stays small and the largest
		// are not crowded out by the first.
		s.opts.Reduce(s.current.Counts)
	}
	if err := writeCheckpoint(s.checkpointPath(), s.current); err != nil {
		s.fail(err)
		return
	}
	if s.problem != "" {
		s.log.Info("statistics are being written again")
		s.problem = ""
	}
}

// collect calls Collect and survives its failure.
func (s *Store) collect() (totals, added map[string]uint64) {
	defer func() {
		if recover() != nil {
			totals, added = map[string]uint64{}, nil
		}
	}()
	if s.opts.Collect != nil {
		totals = s.opts.Collect()
	}
	if totals == nil {
		totals = map[string]uint64{}
	}
	if s.opts.Added != nil {
		added = s.opts.Added()
	}
	return totals, added
}

// fail notes a problem and says it once. The caller holds the lock.
func (s *Store) fail(err error) {
	if s.problem != err.Error() {
		s.log.Warn("statistics could not be written; requests are not affected", "error", err.Error())
	}
	s.problem = err.Error()
}

// appendHour writes a finished hour to its month's file. An hour in which
// nothing was counted is not written.
func (s *Store) appendHour(b Bucket) error {
	if len(b.Counts) == 0 {
		return nil
	}
	if s.opts.Reduce != nil {
		s.opts.Reduce(b.Counts)
	}
	line, err := json.Marshal(b)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.monthPath(b.Hour), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.New("the month's file cannot be opened for writing")
	}
	_, err = f.Write(append(line, '\n'))
	if syncErr := f.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return errors.New("the month's file cannot be written")
	}
	return nil
}

// clean removes the files of months that lie wholly before the time
// statistics are kept. The caller holds the lock.
func (s *Store) clean() {
	s.cleaned = hourOf(s.opts.Now())
	oldest := s.opts.Now().UTC().AddDate(0, 0, -s.opts.KeepDays)
	entries, err := os.ReadDir(s.opts.Dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "hours-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		month, err := time.Parse("2006-01", strings.TrimSuffix(strings.TrimPrefix(name, "hours-"), ".jsonl"))
		if err != nil {
			continue
		}
		if month.AddDate(0, 1, 0).Before(oldest) { // the month's last hour is too old
			if os.Remove(filepath.Join(s.opts.Dir, name)) == nil {
				s.log.Info("statistics past their time were removed", "month", month.Format("2006-01"))
			}
		}
	}
}

func readCheckpoint(path string) (*Bucket, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("the file cannot be read")
	}
	var b Bucket
	if err := json.Unmarshal(data, &b); err != nil || b.Hour.IsZero() {
		return nil, errors.New("the file is damaged")
	}
	if b.Counts == nil {
		b.Counts = map[string]uint64{}
	}
	b.Hour = hourOf(b.Hour)
	return &b, nil
}

// writeCheckpoint replaces the file as a whole, so a crash leaves either the
// old state or the new one.
func writeCheckpoint(path string, b Bucket) error {
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "current-*.tmp")
	if err != nil {
		return errors.New("the directory cannot be written to")
	}
	_, err = tmp.Write(data)
	if syncErr := tmp.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return errors.New("the directory cannot be written to")
	}
	return nil
}

// Hours returns the hours from, and including, the hour of from up to the
// hour of to, oldest first. Hours in which nothing was counted are missing.
func (s *Store) Hours(from, to time.Time) []Bucket {
	from, to = hourOf(from), hourOf(to)
	var out []Bucket
	for month := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.UTC); !month.After(to); month = month.AddDate(0, 1, 0) {
		f, err := os.Open(s.monthPath(month))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64<<10), maxLine)
		for scanner.Scan() {
			var b Bucket
			if json.Unmarshal(scanner.Bytes(), &b) != nil || b.Hour.IsZero() {
				continue // a damaged line costs that hour, not the file
			}
			if !b.Hour.Before(from) && !b.Hour.After(to) {
				out = append(out, b)
			}
		}
		_ = f.Close()
	}
	s.mu.Lock()
	if cur := s.current; len(cur.Counts) > 0 && !cur.Hour.Before(from) && !cur.Hour.After(to) {
		copied := Bucket{Hour: cur.Hour, Counts: make(map[string]uint64, len(cur.Counts))}
		for k, v := range cur.Counts {
			copied.Counts[k] = v
		}
		out = append(out, copied)
	}
	s.mu.Unlock()

	// A file can hold an hour twice (written, then a crash before the
	// state was written down, then written again): add them up.
	sort.SliceStable(out, func(a, b int) bool { return out[a].Hour.Before(out[b].Hour) })
	merged := out[:0]
	for _, b := range out {
		if n := len(merged); n > 0 && merged[n-1].Hour.Equal(b.Hour) {
			for k, v := range b.Counts {
				merged[n-1].Counts[k] += v
			}
			continue
		}
		merged = append(merged, b)
	}
	return merged
}

// Health reports whether statistics are being written.
func (s *Store) Health() health.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.problem != "" {
		return health.Status{State: health.Degraded, Detail: "statistics are not being written: " + s.problem + "; requests are not affected"}
	}
	return health.Status{State: health.OK}
}

// Answer is what the Handler serves.
type Answer struct {
	From   time.Time         `json:"from"`
	To     time.Time         `json:"to"`
	Totals map[string]uint64 `json:"totals"`
	Hours  []Bucket          `json:"hours"`
}

// Handler serves the last hours as JSON: ?hours=N, 24 without it.
func (s *Store) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hours := 24
		if raw := r.URL.Query().Get("hours"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > s.opts.KeepDays*24 {
				http.Error(w, fmt.Sprintf("hours must be a number from 1 to %d", s.opts.KeepDays*24), http.StatusBadRequest)
				return
			}
			hours = n
		}
		to := hourOf(s.opts.Now())
		a := Answer{From: to.Add(-time.Duration(hours-1) * time.Hour), To: to, Totals: map[string]uint64{}}
		a.Hours = s.Hours(a.From, a.To)
		if a.Hours == nil {
			a.Hours = []Bucket{}
		}
		for _, b := range a.Hours {
			for k, v := range b.Counts {
				a.Totals[k] += v
			}
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(a)
	})
}
