package preview

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/MaMoja/xibalba/internal/health"
)

func TestParse(t *testing.T) {
	long := strings.Repeat("ä", 2000)
	cases := []struct {
		name, page string
		want       []Tag
	}{
		{"plain", `<html><head><title>T</title>
<meta property="og:title" content="Rathaus &amp; B&uuml;rger">
<meta name="description" content="  Two
 lines ">
<meta name="twitter:card" content=summary>
<meta name="viewport" content="x"></head>`,
			[]Tag{{"og:title", "Rathaus & Bürger", false}, {"description", "Two lines", true}, {"twitter:card", "summary", true}}},
		{"title when no og:title", `<head><TITLE lang="de"> Hello <b>x</b> </TITLE><meta content='A "b"' property='og:description'/></head>`,
			[]Tag{{"og:description", `A "b"`, false}, {"og:title", "Hello <b>x</b>", false}}},
		{"only the head", `<head><meta property="og:title" content="in"></head><body><meta property="og:image" content="out">`,
			[]Tag{{"og:title", "in", false}}},
		{"first of a name wins", `<meta property="og:title" content="one"><meta property="OG:Title" content="two">`,
			[]Tag{{"og:title", "one", false}}},
		{"hostile names and values", `<meta property='og:x"><script>' content="v"><meta property="og:image" content="&#x22;&gt;&lt;script&gt;alert(1)&lt;/script&gt;">
<meta property="og:a` + "\x00" + `" content="v"><meta property="og:e" content=""><meta property="og:` + strings.Repeat("k", 80) + `" content="v">`,
			[]Tag{{"og:image", `"><script>alert(1)</script>`, false}}},
		{"greater-than sign in a value", `<meta property="og:title" content="Home > Town"><meta name="description" content='<script>alert(1)</script>'>`,
			[]Tag{{"og:title", "Home > Town", false}, {"description", "<script>alert(1)</script>", true}}},
		{"comments, scripts and the body are not the page's own tags", `<head><!-- <meta property="og:title" content="old"> --><script>var s = '<meta property="og:title" content="js">';</script>
<noscript><meta property="og:image" content="n"></noscript><meta property="og:title" content="real"><body><meta property="og:description" content="from a comment field">`,
			[]Tag{{"og:title", "real", false}}},
		{"unfinished comment and script", `<meta property="og:title" content="a"><script`, []Tag{{"og:title", "a", false}}},
		{"unfinished", `<meta property="og:title" content="never closed`, nil},
		{"unfinished quote", `<meta property="og:title content=x><title>t`, nil},
		{"nothing", "", nil},
		{"control characters and bad UTF-8", "<meta property=og:title content=\"a\x01b\xffc\x7fd\">",
			[]Tag{{"og:title", "a bc d", false}}},
		{"long value is cut at a character", `<meta property="og:title" content="` + long + `">`,
			[]Tag{{"og:title", strings.Repeat("ä", MaxValue/2), false}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Parse(c.page)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got  %+v\nwant %+v", got, c.want)
			}
			for _, tag := range got {
				if !utf8.ValidString(tag.Value) || len(tag.Value) > MaxValue {
					t.Errorf("value not bounded plain text: %q", tag.Value)
				}
			}
		})
	}
}

func TestParseIsBounded(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&b, `<meta property="og:t%d" content="v" a=1 b=2 c=3 d=4 e=5 f=6 g=7 h=8 i=9 j=10 k=11 l=12 m=13 n=14 o=15 p=16 q=17 r=18>`, i)
	}
	if got := Parse(b.String()); len(got) != MaxTags {
		t.Errorf("%d tags, want %d", len(got), MaxTags)
	}
}

func FuzzParse(f *testing.F) {
	f.Add(`<meta property="og:title" content="x"><title>t</title>`)
	f.Add(`<meta name=description content='`)
	f.Fuzz(func(t *testing.T, page string) {
		for _, tag := range Parse(page) {
			if !plainKey(tag.Key) || tag.Value == "" || len(tag.Value) > MaxValue || !utf8.ValidString(tag.Value) {
				t.Fatalf("bad tag %+v", tag)
			}
		}
	})
}

// site is a website that counts what it is asked.
type site struct {
	*httptest.Server
	hits  atomic.Int64
	last  atomic.Value // *http.Request
	reply func(w http.ResponseWriter, r *http.Request)
}

func newSite(t *testing.T) *site {
	s := &site{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		s.last.Store(r)
		if s.reply != nil {
			s.reply(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<head><meta property="og:title" content="%s"></head>`, r.URL.RequestURI())
	}))
	t.Cleanup(s.Close)
	return s
}

func start(t *testing.T, s *site, change func(*Options)) *Cache {
	u, _ := url.Parse(s.URL)
	opts := Options{Upstream: u, TTL: time.Hour, MaxEntries: 100, PerMinute: 60000, UserAgent: "Xibalba-test"}
	if change != nil {
		change(&opts)
	}
	c := New(opts)
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
	return c
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("never happened: %s", what)
}

func TestTagsAreFetchedInTheBackground(t *testing.T) {
	s := newSite(t)
	c := start(t, s, nil)
	if got := c.Tags("www.example.org", "/a", "utm=1"); got != nil {
		t.Fatalf("first call has tags: %+v", got)
	}
	eventually(t, "tags known", func() bool { return len(c.Tags("www.example.org", "/a", "other=2")) == 1 })
	if got := c.Tags("h", "/a", ""); got[0].Value != "/a" {
		t.Errorf("the query was sent to the website: %+v", got)
	}
	if n := s.hits.Load(); n != 1 {
		t.Errorf("%d fetches, want 1", n)
	}
	r := s.last.Load().(*http.Request)
	if r.Header.Get("User-Agent") != "Xibalba-test" || r.Header.Get("Cookie") != "" || r.Method != "GET" {
		t.Errorf("request: %s %v", r.Method, r.Header)
	}
	if st := c.Health(); st.State != health.OK {
		t.Errorf("health: %+v", st)
	}
}

func TestQueryAndHostKeepPagesApart(t *testing.T) {
	s := newSite(t)
	c := start(t, s, func(o *Options) { o.Query, o.PreserveHost = true, true })
	c.Tags("a.example", "/p", "id=1")
	c.Tags("b.example", "/p", "id=2")
	eventually(t, "both known", func() bool {
		return len(c.Tags("a.example", "/p", "id=1")) == 1 && len(c.Tags("B.example", "/p", "id=2")) == 1
	})
	if got := c.Tags("a.example", "/p", "id=1")[0].Value; got != "/p?id=1" {
		t.Errorf("got %q", got)
	}
	if r := s.last.Load().(*http.Request); r.Host != "b.example" {
		t.Errorf("host sent: %q", r.Host)
	}
	if c.Tags("a.example", "/p", "id=2") != nil {
		t.Error("tags of another host's page")
	}
}

func TestAddressesNotFetched(t *testing.T) {
	s := newSite(t)
	c := start(t, s, func(o *Options) { o.Query = true })
	for _, a := range [][3]string{
		{"h", "/../etc", ""}, {"h", "/a/./b", ""}, {"h", "relative", ""}, {"h", "", ""}, {"h", "/a\nb", ""},
		{"h", "/" + strings.Repeat("a", 2000), ""}, {"h", "/a", strings.Repeat("q", 600)}, {strings.Repeat("h", 300), "/a", ""},
	} {
		if c.Tags(a[0], a[1], a[2]) != nil {
			t.Errorf("tags for %q", a)
		}
	}
	c.Tags("h", "/real", "")
	eventually(t, "the real one", func() bool { return s.hits.Load() == 1 })
	time.Sleep(20 * time.Millisecond)
	if n := s.hits.Load(); n != 1 {
		t.Errorf("%d fetches, want 1", n)
	}
}

func TestAnswersThatGiveNoTags(t *testing.T) {
	cases := map[string]func(w http.ResponseWriter, r *http.Request){
		"redirect": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://127.0.0.1:1/elsewhere", http.StatusFound)
		},
		"not html": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`<meta property="og:title" content="x">`))
		},
		"not found": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`<meta property="og:title" content="x">`))
		},
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			s := newSite(t)
			s.reply = reply
			c := start(t, s, nil)
			c.Tags("h", "/x", "")
			eventually(t, "fetched", func() bool { f, _, _ := c.Counts(); return f == 1 })
			time.Sleep(10 * time.Millisecond)
			if got := c.Tags("h", "/x", ""); got != nil {
				t.Errorf("tags: %+v", got)
			}
			if n := s.hits.Load(); n != 1 {
				t.Errorf("%d fetches: the answer was not remembered, or a redirect was followed", n)
			}
		})
	}
}

func TestOnlyTheStartOfAPageIsRead(t *testing.T) {
	s := newSite(t)
	s.reply = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<meta property="og:title" content="early">` + strings.Repeat(" ", maxHead) + `<meta property="og:image" content="late">`))
	}
	c := start(t, s, nil)
	c.Tags("h", "/", "")
	eventually(t, "known", func() bool { return len(c.Tags("h", "/", "")) > 0 })
	if got := c.Tags("h", "/", ""); len(got) != 1 || got[0].Value != "early" {
		t.Errorf("got %+v", got)
	}
}

func TestWebsiteDown(t *testing.T) {
	s := newSite(t)
	u, _ := url.Parse(s.URL)
	s.Close()
	c := New(Options{Upstream: u, TTL: time.Hour, MaxEntries: 10, PerMinute: 60000, Timeout: time.Second})
	_ = c.Start(context.Background())
	defer func() { _ = c.Stop(context.Background()) }()
	c.Tags("h", "/", "")
	eventually(t, "failure counted", func() bool { _, failed, _ := c.Counts(); return failed == 1 })
	st := c.Health()
	if st.State != health.Degraded || strings.Contains(st.Detail, "127.0.0.1") {
		t.Errorf("health: %+v", st)
	}
}

func TestTheTableAndTheLineAreBounded(t *testing.T) {
	s := newSite(t)
	block := make(chan struct{})
	s.reply = func(http.ResponseWriter, *http.Request) { <-block }
	defer close(block)
	now := time.Unix(1_700_000_000, 0)
	c := start(t, s, func(o *Options) { o.MaxEntries = 50; o.Now = func() time.Time { return now } })
	began := time.Now()
	for i := 0; i < 5000; i++ {
		c.Tags("h", fmt.Sprintf("/%d", i), "")
	}
	if took := time.Since(began); took > 2*time.Second {
		t.Errorf("Tags waited: %s", took)
	}
	c.mu.Lock()
	n := len(c.entries)
	c.mu.Unlock()
	if n > 50 {
		t.Errorf("%d entries, limit 50", n)
	}
	if _, _, dropped := c.Counts(); dropped == 0 {
		t.Error("nothing counted as dropped")
	}
	if hits := s.hits.Load(); hits > 1 {
		t.Errorf("%d fetches at once, want one at a time", hits)
	}
}

func TestFetchesAreSpaced(t *testing.T) {
	s := newSite(t)
	c := start(t, s, func(o *Options) { o.PerMinute = 60 }) // one a second
	for i := 0; i < 10; i++ {
		c.Tags("h", fmt.Sprintf("/%d", i), "")
	}
	time.Sleep(300 * time.Millisecond)
	if n := s.hits.Load(); n != 1 {
		t.Errorf("%d fetches in 0.3 s at one a second", n)
	}
}

func TestTagsExpireAndStayInUseMeanwhile(t *testing.T) {
	s := newSite(t)
	var now atomic.Int64
	now.Store(1_700_000_000)
	c := start(t, s, func(o *Options) { o.Now = func() time.Time { return time.Unix(now.Load(), 0) } })
	c.Tags("h", "/a", "")
	eventually(t, "known", func() bool { return len(c.Tags("h", "/a", "")) == 1 })
	now.Add(3601)
	if got := c.Tags("h", "/a", ""); len(got) != 1 {
		t.Errorf("old tags not used while fetching again: %+v", got)
	}
	eventually(t, "fetched again", func() bool { return s.hits.Load() == 2 })
}

func TestFixed(t *testing.T) {
	s := newSite(t)
	fixed := Fixed(map[string]string{"og:title": "Town", "description": "D", "twitter:card": "summary"})
	want := []Tag{{"description", "D", true}, {"og:title", "Town", false}, {"twitter:card", "summary", true}}
	if !reflect.DeepEqual(fixed, want) {
		t.Fatalf("got %+v", fixed)
	}
	c := start(t, s, func(o *Options) { o.Fixed = fixed })
	if got := c.Tags("h", "/anything", ""); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	time.Sleep(20 * time.Millisecond)
	if s.hits.Load() != 0 {
		t.Error("fetched although the tags are fixed")
	}
}

func TestCheckTag(t *testing.T) {
	good := [][2]string{{"og:title", "Town hall"}, {"description", "x"}, {"twitter:card", "summary"}, {"article:author", "A"}}
	bad := [][2]string{{"title", "x"}, {"OG:title", "x"}, {`og:x"`, "x"}, {"og:title", ""}, {"og:title", "two\nlines"},
		{"og:title", strings.Repeat("x", MaxValue+1)}, {"og:title", " padded "}, {"robots", "index"}}
	for _, c := range good {
		if msg := CheckTag(c[0], c[1]); msg != "" {
			t.Errorf("%q: %s", c, msg)
		}
	}
	for _, c := range bad {
		if CheckTag(c[0], c[1]) == "" {
			t.Errorf("%q accepted", c)
		}
	}
}

func TestTagsOfAPageAreBoundedAndCopied(t *testing.T) {
	var b strings.Builder
	for i := 0; i < MaxTags; i++ {
		fmt.Fprintf(&b, `<meta property="og:t%d" content="%s">`, i, strings.Repeat("x", MaxValue))
	}
	page := b.String()
	total := 0
	for _, tag := range Parse(page) {
		total += len(tag.Key) + len(tag.Value)
		at, from := uintptr(unsafe.Pointer(unsafe.StringData(tag.Value))), uintptr(unsafe.Pointer(unsafe.StringData(page)))
		if at >= from && at < from+uintptr(len(page)) {
			t.Fatal("a value is a piece of the page and keeps the page in memory")
		}
	}
	if total == 0 || total > MaxBytes {
		t.Errorf("%d bytes of tags, limit %d", total, MaxBytes)
	}
}

func TestAVisitorsHostNameDoesNotReachOtherVisitors(t *testing.T) {
	s := newSite(t)
	s.reply = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<meta property="og:image" content="https://%s%s/logo.png">`, r.Header.Get("X-Forwarded-Host"), r.Header.Get("Forwarded"))
	}
	c := start(t, s, nil) // the website is not told the host name
	c.Tags("evil.example", "/rathaus", "")
	eventually(t, "known", func() bool { return len(c.Tags("www.example.org", "/rathaus", "")) == 1 })
	if got := c.Tags("www.example.org", "/rathaus", "")[0].Value; got != "https:///logo.png" {
		t.Errorf("the first visitor's host name is in everyone's tags: %q", got)
	}
	if r := s.last.Load().(*http.Request); strings.Contains(r.Host, "evil") {
		t.Errorf("host sent: %q", r.Host)
	}
}

func TestPagesWithTagsAreNotPushedOut(t *testing.T) {
	s := newSite(t)
	s.reply = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if strings.HasPrefix(r.URL.Path, "/real") {
			_, _ = w.Write([]byte(`<meta property="og:title" content="real">`))
		}
	}
	c := start(t, s, func(o *Options) { o.MaxEntries = 20 })
	for i := 0; i < 20; i++ {
		path := fmt.Sprintf("/real%d", i)
		c.Tags("h", path, "")
		eventually(t, path, func() bool { return len(c.Tags("h", path, "")) == 1 })
	}
	for i := 0; i < 5000; i++ {
		c.Tags("h", fmt.Sprintf("/junk%d", i), "")
	}
	for i := 0; i < 20; i++ {
		if len(c.Tags("h", fmt.Sprintf("/real%d", i), "")) != 1 {
			t.Fatalf("/real%d was pushed out by addresses without tags", i)
		}
	}
	c.mu.Lock()
	n := len(c.entries)
	c.mu.Unlock()
	if n > 20 {
		t.Errorf("%d entries, limit 20", n)
	}
}

func TestOldTagsSurviveAFailedFetch(t *testing.T) {
	s := newSite(t)
	var down atomic.Bool
	s.reply = func(w http.ResponseWriter, _ *http.Request) {
		if down.Load() {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<meta property="og:title" content="t">`))
	}
	var now atomic.Int64
	now.Store(1_700_000_000)
	c := start(t, s, func(o *Options) { o.Now = func() time.Time { return time.Unix(now.Load(), 0) } })
	c.Tags("h", "/a", "")
	eventually(t, "known", func() bool { return len(c.Tags("h", "/a", "")) == 1 })
	down.Store(true)
	now.Add(3601)
	c.Tags("h", "/a", "")
	eventually(t, "second fetch", func() bool { return s.hits.Load() == 2 })
	time.Sleep(20 * time.Millisecond)
	if len(c.Tags("h", "/a", "")) != 1 {
		t.Error("a failed fetch threw the old tags away")
	}
}

func TestSkippedPathsAndEscapedPaths(t *testing.T) {
	s := newSite(t)
	c := start(t, s, func(o *Options) { o.Skip = []string{"/intern/"} })
	for _, path := range []string{"/intern/plan", "/%69ntern/plan", "/a/%2e%2e/intern/x", "/a%3Fb?c", "/bad%zz"} {
		if c.Tags("h", path, "") != nil {
			t.Errorf("tags for %s", path)
		}
	}
	time.Sleep(30 * time.Millisecond)
	if n := s.hits.Load(); n != 0 {
		t.Fatalf("%d fetches for paths not to fetch", n)
	}
	c.Tags("h", "/a%2Fb%20c", "")
	eventually(t, "fetched", func() bool { return s.hits.Load() == 1 })
	if got := s.last.Load().(*http.Request).URL.EscapedPath(); got != "/a%2Fb%20c" {
		t.Errorf("the website was asked for %s", got)
	}
}

func BenchmarkTagsOnAFullTable(b *testing.B) {
	u, _ := url.Parse("http://127.0.0.1:1")
	c := New(Options{Upstream: u, TTL: time.Hour, MaxEntries: 20000, PerMinute: 1})
	for i := 0; i < 20000; i++ {
		c.entries[fmt.Sprintf("/p%d?", i)] = entry{tags: []Tag{{Key: "og:title", Value: "t"}}, expires: time.Now().Add(time.Hour)}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Tags("h", "/junk"+fmt.Sprint(i), "")
	}
}
