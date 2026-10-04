package stats

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MaMoja/xibalba/internal/health"
)

// world is a clock and a set of running totals that a test can move.
type world struct {
	mu     sync.Mutex
	now    time.Time
	totals map[string]uint64
}

func newWorld() *world {
	return &world{now: time.Date(2026, 10, 4, 9, 10, 0, 0, time.UTC), totals: map[string]uint64{}}
}

func (w *world) Now() time.Time            { w.mu.Lock(); defer w.mu.Unlock(); return w.now }
func (w *world) advance(d time.Duration)   { w.mu.Lock(); w.now = w.now.Add(d); w.mu.Unlock() }
func (w *world) add(name string, n uint64) { w.mu.Lock(); w.totals[name] += n; w.mu.Unlock() }
func (w *world) restart()                  { w.mu.Lock(); w.totals = map[string]uint64{}; w.mu.Unlock() }
func (w *world) collect() map[string]uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]uint64, len(w.totals))
	for k, v := range w.totals {
		out[k] = v
	}
	return out
}

func start(t *testing.T, w *world, dir string, keep int) *Store {
	t.Helper()
	s := New(Options{Dir: dir, KeepDays: keep, Collect: w.collect, Every: time.Hour, Now: w.Now})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

func stop(t *testing.T, s *Store) {
	t.Helper()
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func counts(t *testing.T, s *Store, w *world, hours int) map[string]map[string]uint64 {
	t.Helper()
	out := map[string]map[string]uint64{}
	for _, b := range s.Hours(w.Now().Add(-time.Duration(hours)*time.Hour), w.Now()) {
		out[b.Hour.Format("15h")] = b.Counts
	}
	return out
}

func TestCountsAreKeptByTheHour(t *testing.T) {
	w := newWorld()
	s := start(t, w, t.TempDir(), 30)
	defer stop(t, s)

	w.add("deny", 3)
	w.add("allow", 10)
	s.Sample()
	w.add("deny", 2)
	s.Sample()
	s.Sample() // nothing new: nothing added

	w.advance(time.Hour) // 10:10
	w.add("deny", 1)
	s.Sample()

	w.advance(3 * time.Hour) // 13:10, nothing happened at 11 and 12
	w.add("allow", 5)
	s.Sample()

	got := counts(t, s, w, 10)
	if got["09h"]["deny"] != 5 || got["09h"]["allow"] != 10 || got["10h"]["deny"] != 1 || got["13h"]["allow"] != 5 {
		t.Errorf("hours = %v", got)
	}
	if _, ok := got["11h"]; ok || len(got) != 3 {
		t.Errorf("hours without counts are stored: %v", got)
	}
	if _, zero := got["10h"]["allow"]; zero {
		t.Errorf("a name with nothing added is stored: %v", got["10h"])
	}
}

func TestCountsSurviveARestart(t *testing.T) {
	w := newWorld()
	dir := t.TempDir()
	s := start(t, w, dir, 30)
	w.add("deny", 4)
	s.Sample()
	w.add("deny", 1) // counted, but not looked at before the stop
	stop(t, s)

	// The program starts again within the same hour: its totals start at zero.
	w.restart()
	w.advance(10 * time.Minute)
	s = start(t, w, dir, 30)
	w.add("deny", 2)
	s.Sample()
	if got := counts(t, s, w, 2)["09h"]["deny"]; got != 7 {
		t.Errorf("after a restart in the same hour: %d, want 7", got)
	}
	stop(t, s)

	// And again, hours later: the hour it stopped in is finished and kept.
	w.restart()
	w.advance(5 * time.Hour)
	s = start(t, w, dir, 30)
	defer stop(t, s)
	w.add("deny", 1)
	s.Sample()
	got := counts(t, s, w, 10)
	if got["09h"]["deny"] != 7 || got["14h"]["deny"] != 1 {
		t.Errorf("after a later restart: %v", got)
	}
}

func TestOldMonthsAreRemoved(t *testing.T) {
	w := newWorld() // 4 October 2026
	dir := t.TempDir()
	for _, month := range []string{"2026-06", "2026-07", "2026-08", "2026-09"} {
		line := `{"hour":"` + month + `-15T10:00:00Z","counts":{"deny":1}}` + "\n"
		if err := os.WriteFile(filepath.Join(dir, "hours-"+month+".jsonl"), []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("mine"), 0o600) // not ours: left alone
	s := start(t, w, dir, 60)                                                // keep 60 days: back to 5 August
	defer stop(t, s)
	var left []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if got := strings.Join(left, " "); got != "hours-2026-08.jsonl hours-2026-09.jsonl notes.txt" {
		t.Errorf("files left: %s", got)
	}
}

func TestDamageCostsAnHourNotTheStatistics(t *testing.T) {
	w := newWorld()
	dir := t.TempDir()
	month := filepath.Join(dir, "hours-2026-10.jsonl")
	content := `{"hour":"2026-10-04T06:00:00Z","counts":{"deny":1}}
this line is damaged
{"hour":"2026-10-04T07:00:00Z","counts":{"deny":2}}
{"hour":"2026-10-04T07:00:00Z","counts":{"deny":3}}
{"hour":"2026-10-04T08:00:00Z","counts":
`
	_ = os.WriteFile(month, []byte(content), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "current.json"), []byte("{broken"), 0o600)

	s := start(t, w, dir, 30)
	defer stop(t, s)
	got := counts(t, s, w, 10)
	// An hour that was written twice is added up.
	if got["06h"]["deny"] != 1 || got["07h"]["deny"] != 5 || len(got) != 2 {
		t.Errorf("hours = %v", got)
	}
	w.add("deny", 9)
	s.Sample()
	if counts(t, s, w, 10)["09h"]["deny"] != 9 || s.Health().State != health.OK {
		t.Errorf("after damage: %v, %+v", counts(t, s, w, 10), s.Health())
	}
}

func TestADirectoryThatCannotBeWrittenIsReportedNotFatal(t *testing.T) {
	w := newWorld()
	dir := filepath.Join(t.TempDir(), "gone")
	s := New(Options{Dir: dir, KeepDays: 30, Collect: w.collect, Every: time.Hour, Now: w.Now})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer stop(t, s)
	w.add("deny", 1)
	s.Sample()
	if h := s.Health(); h.State != health.Degraded || !strings.Contains(h.Detail, "requests are not affected") {
		t.Errorf("health = %+v", h)
	}
	// The directory appears: counting carries on, and nothing counted meanwhile is lost.
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	w.add("deny", 1)
	s.Sample()
	if h := s.Health(); h.State != health.OK {
		t.Errorf("health = %+v", h)
	}
	if got := counts(t, s, w, 1)["09h"]["deny"]; got != 2 {
		t.Errorf("deny = %d, want 2", got)
	}
}

func TestACollectorThatFailsOrResets(t *testing.T) {
	w := newWorld()
	fail := false
	s := New(Options{Dir: t.TempDir(), KeepDays: 30, Every: time.Hour, Now: w.Now, Collect: func() map[string]uint64 {
		if fail {
			panic("broken")
		}
		return w.collect()
	}})
	_ = s.Start(context.Background())
	defer stop(t, s)
	w.add("deny", 5)
	s.Sample()
	fail = true
	s.Sample() // must not panic
	fail = false
	// A counter that starts again below its last value is taken as new.
	w.restart()
	w.add("deny", 2)
	s.Sample()
	if got := counts(t, s, w, 1)["09h"]["deny"]; got != 7 {
		t.Errorf("deny = %d, want 7", got)
	}
}

func TestHandler(t *testing.T) {
	w := newWorld()
	s := start(t, w, t.TempDir(), 30)
	defer stop(t, s)
	w.add("decision|default|allow", 4)
	s.Sample()
	w.advance(time.Hour)
	w.add("decision|default|allow", 6)
	s.Sample()

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/statistics?hours=3", nil))
	var a Answer
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if len(a.Hours) != 2 || a.Totals["decision|default|allow"] != 10 || a.To.Sub(a.From) != 2*time.Hour {
		t.Errorf("answer = %+v", a)
	}
	for _, bad := range []string{"0", "-1", "x", "99999999"} {
		rec = httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/statistics?hours="+bad, nil))
		if rec.Code != 400 {
			t.Errorf("hours=%s: status %d", bad, rec.Code)
		}
	}
	// Files are readable by their owner only.
	entries, _ := os.ReadDir(s.opts.Dir)
	for _, e := range entries {
		if info, _ := e.Info(); info.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %v", e.Name(), info.Mode().Perm())
		}
	}
}

func TestBackgroundSamplingAndConcurrentReads(t *testing.T) {
	w := newWorld()
	s := New(Options{Dir: t.TempDir(), KeepDays: 30, Collect: w.collect, Every: 2 * time.Millisecond, Now: w.Now})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				w.add("deny", 1)
				s.Hours(w.Now().Add(-time.Hour), w.Now())
				s.Health()
			}
		}()
	}
	wg.Wait()
	stop(t, s) // the last look happens at the stop
	if got := counts(t, s, w, 1)["09h"]["deny"]; got != 800 {
		t.Errorf("deny = %d, want 800", got)
	}
	if s.Name() != "statistics" {
		t.Errorf("Name() = %q", s.Name())
	}
}
