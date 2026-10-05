package geo

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MaMoja/xibalba/internal/health"
)

// Limits and periods.
const (
	// MaxFileSize is the largest database file that is read.
	MaxFileSize = 512 << 20
	// watchEvery is how often the file is checked for a newer version.
	watchEvery = time.Minute
	// downloadEvery is how often a download is considered.
	downloadEvery = 6 * time.Hour
	// refreshAfter is the age at which a downloaded file is replaced. The
	// free databases appear monthly.
	refreshAfter = 30 * 24 * time.Hour
	// staleAfter is the age of the data at which health reports it.
	staleAfter      = 100 * 24 * time.Hour
	downloadTimeout = 5 * time.Minute
)

// Options configures a Locator.
type Options struct {
	// Name is the component's name. Empty means "countries".
	Name string
	// What the database holds, for messages. Empty means "country".
	// "network" for a database of network operators.
	What string
	// Path is the database file.
	Path string
	// Download fetches the database from DownloadURL when the file is
	// missing or older than a month.
	Download bool
	// DownloadURL is where the database is fetched from. "{year}" and
	// "{month}" are replaced by the current year and two-digit month. The
	// answer may be compressed with gzip.
	DownloadURL string
	// UserAgent is sent when downloading.
	UserAgent string
	// Client downloads the file. Nil means a client with a time limit.
	Client *http.Client
	// Log receives the locator's messages.
	Log *slog.Logger
	// Now returns the current time. Tests replace it; nil means time.Now.
	Now func() time.Time
}

// Locator keeps the database loaded and current. It is a lifecycle component.
type Locator struct {
	opts Options
	log  *slog.Logger
	db   atomic.Pointer[DB]

	mu       sync.Mutex
	loaded   time.Time // modification time of the file that is loaded
	problem  string    // why the file could not be loaded or downloaded
	lastTry  time.Time // last download attempt
	attempts int

	cancel context.CancelFunc
	done   chan struct{}
}

// New returns a Locator. It reads nothing until Start.
func New(opts Options) *Locator {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Client == nil {
		opts.Client = &http.Client{
			Timeout: downloadTimeout,
			// A redirect must not lead away from a protected connection.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("too many redirects")
				}
				if CheckURL(req.URL.String()) != nil {
					return errors.New("redirected to an address that cannot be used")
				}
				return nil
			},
		}
	}
	if opts.Name == "" {
		opts.Name = "countries"
	}
	if opts.What == "" {
		opts.What = "country"
	}
	return &Locator{opts: opts, log: opts.Log.With("component", opts.Name)}
}

// Name implements lifecycle.Component.
func (l *Locator) Name() string { return l.opts.Name }

// Start loads the database and keeps watching the file. A file that is
// missing or broken does not stop Xibalba: countries are then not known,
// and health says so.
func (l *Locator) Start(context.Context) error {
	l.reload()
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel, l.done = cancel, make(chan struct{})
	go func() {
		defer close(l.done)
		l.maybeDownload(ctx)
		l.reload() // use a fresh download at once, not a minute later
		ticker := time.NewTicker(watchEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				l.maybeDownload(ctx)
				l.reload()
			}
		}
	}()
	return nil
}

// Stop ends the background work.
func (l *Locator) Stop(ctx context.Context) error {
	if l.cancel == nil {
		return nil
	}
	l.cancel()
	select {
	case <-l.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Country returns the country of an address, or the zero Code if it is not
// known or no database is loaded.
func (l *Locator) Country(addr netip.Addr) Code {
	db := l.db.Load()
	if db == nil {
		return Code{}
	}
	return db.Country(addr)
}

// ASN returns the number of the network operator of an address, or 0 if it
// is not known or no database is loaded.
func (l *Locator) ASN(addr netip.Addr) uint32 {
	db := l.db.Load()
	if db == nil {
		return 0
	}
	return db.ASN(addr)
}

// CheckURL says whether a download address can be used: https, or plain
// http to this machine itself. A database fetched over an unprotected
// connection could be replaced on the way.
func CheckURL(raw string) error {
	u, err := url.Parse(strings.NewReplacer("{year}", "2026", "{month}", "01").Replace(raw))
	if err != nil || u.Host == "" {
		return errors.New("it is not a web address")
	}
	if u.User != nil {
		return errors.New("it must not contain a user name or password")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" {
		if host := u.Hostname(); host == "localhost" {
			return nil
		} else if addr, err := netip.ParseAddr(host); err == nil && addr.IsLoopback() {
			return nil
		}
	}
	return errors.New("it must start with https://")
}

// Loaded reports whether a database is loaded.
func (l *Locator) Loaded() bool { return l.db.Load() != nil }

// ReadFile reads and checks a database file.
func ReadFile(path string) (*DB, time.Time, error) {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, time.Time{}, errors.New("the file does not exist")
	case err != nil:
		return nil, time.Time{}, errors.New("the file cannot be opened")
	case !info.Mode().IsRegular():
		return nil, time.Time{}, errors.New("it is not a regular file")
	case info.Size() > MaxFileSize:
		return nil, time.Time{}, fmt.Errorf("the file is larger than %d MiB", MaxFileSize>>20)
	}
	// The size was checked above, but the file may grow in between.
	file, err := os.Open(path)
	if err != nil {
		return nil, time.Time{}, errors.New("the file cannot be read")
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxFileSize+1))
	_ = file.Close()
	if err != nil || len(data) > MaxFileSize {
		return nil, time.Time{}, errors.New("the file cannot be read")
	}
	db, err := Open(data)
	if err != nil {
		return nil, time.Time{}, err
	}
	return db, info.ModTime(), nil
}

// reload loads the file if it is new or has changed. A file that cannot be
// used leaves the database that is loaded in place.
func (l *Locator) reload() {
	info, err := os.Stat(l.opts.Path)
	l.mu.Lock()
	unchanged := err == nil && l.db.Load() != nil && info.ModTime().Equal(l.loaded)
	l.mu.Unlock()
	if unchanged {
		return
	}
	db, modified, err := ReadFile(l.opts.Path)

	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil {
		if l.problem != err.Error() { // say it once
			l.log.Warn("the "+l.opts.What+" database cannot be used", "error", err.Error(), "kept_previous", l.db.Load() != nil)
		}
		l.problem = err.Error()
		return
	}
	l.db.Store(db)
	l.loaded, l.problem = modified, ""
	l.log.Info(l.opts.What+" database loaded", "type", db.Type, "built", db.Built.Format("2006-01-02"))
}

// maybeDownload fetches the database if downloading is on and the file is
// missing or a month old. After a failure it waits longer each time.
func (l *Locator) maybeDownload(ctx context.Context) {
	if !l.opts.Download {
		return
	}
	now := l.opts.Now()
	if info, err := os.Stat(l.opts.Path); err == nil && now.Sub(info.ModTime()) < refreshAfter {
		// Fresh enough, if it is usable. A file that was cut off (a power
		// cut during the last download, say) is fetched again.
		l.mu.Lock()
		usable := l.db.Load() != nil && l.problem == ""
		l.mu.Unlock()
		if usable {
			return
		}
		if _, _, err := ReadFile(l.opts.Path); err == nil {
			return
		}
	}
	l.mu.Lock()
	wait := downloadEvery
	if l.attempts > 0 && l.attempts < 4 {
		wait = time.Duration(l.attempts) * 5 * time.Minute
	}
	due := l.lastTry.IsZero() || now.Sub(l.lastTry) >= wait
	if due {
		l.lastTry = now
		l.attempts++
	}
	l.mu.Unlock()
	if !due {
		return
	}

	err := l.download(ctx, now)
	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil {
		problem := "download failed: " + err.Error()
		if l.problem != problem { // say it once
			l.log.Warn("the "+l.opts.What+" database could not be downloaded", "error", err.Error())
		}
		l.problem = problem
		return
	}
	l.attempts = 0
	l.problem = ""
	l.log.Info(l.opts.What + " database downloaded")
}

func (l *Locator) download(ctx context.Context, now time.Time) error {
	address := strings.NewReplacer("{year}", now.UTC().Format("2006"), "{month}", now.UTC().Format("01")).Replace(l.opts.DownloadURL)
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return errors.New("the download address is not valid")
	}
	req.Header.Set("User-Agent", l.opts.UserAgent)
	resp, err := l.opts.Client.Do(req)
	if err != nil {
		return errors.New("the server could not be reached")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the server answered with status %d", resp.StatusCode)
	}

	tmp, err := os.CreateTemp(filepath.Dir(l.opts.Path), "."+l.opts.Name+"-*.tmp")
	if err != nil {
		return errors.New("the directory of the database file is not writable")
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no effect once renamed

	// The first two bytes say whether the answer is compressed.
	head := make([]byte, 2)
	n, _ := io.ReadFull(resp.Body, head)
	body := io.MultiReader(strings.NewReader(string(head[:n])), resp.Body)
	if n == 2 && head[0] == 0x1f && head[1] == 0x8b {
		zr, err := gzip.NewReader(body)
		if err != nil {
			_ = tmp.Close()
			return errors.New("the download is damaged")
		}
		body = zr
	}
	written, err := io.Copy(tmp, io.LimitReader(body, MaxFileSize+1))
	// On disk before it takes the place of the old file: after a power cut
	// there must be the old file or the new one, never half of one.
	if syncErr := tmp.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return errors.New("the download was interrupted or is damaged")
	}
	if written > MaxFileSize {
		return fmt.Errorf("the download is larger than %d MiB", MaxFileSize>>20)
	}
	// Only a file that is a usable database replaces the one in place.
	if _, _, err := ReadFile(tmp.Name()); err != nil {
		return fmt.Errorf("the download is not a usable database: %v", err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return errors.New("the database file could not be put in place")
	}
	if err := os.Rename(tmp.Name(), l.opts.Path); err != nil {
		return errors.New("the database file could not be put in place")
	}
	return nil
}

// Health reports whether countries are known.
func (l *Locator) Health() health.Status {
	db := l.db.Load()
	l.mu.Lock()
	problem := l.problem
	l.mu.Unlock()
	switch {
	case db == nil:
		detail := "no " + l.opts.What + " database is loaded; rules with a condition on it are skipped until one is"
		if problem != "" {
			detail += ": " + problem
		}
		return health.Status{State: health.Degraded, Detail: detail}
	case problem != "":
		return health.Status{State: health.Degraded, Detail: "the " + l.opts.What + " database from " + db.Built.Format("2006-01-02") + " stays in use: " + problem}
	case !db.Built.IsZero() && l.opts.Now().Sub(db.Built) > staleAfter:
		return health.Status{State: health.Degraded, Detail: "the " + l.opts.What + " database is from " + db.Built.Format("2006-01-02") + "; addresses change hands, replace it with a current one"}
	}
	return health.Status{State: health.OK}
}
