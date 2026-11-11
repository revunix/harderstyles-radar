package hardstyle

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func init() { PageDelay, DetailDelay, RetryDelay = 0, 0, 0 }

func TestParseDetails(t *testing.T) {
	page := []byte(`<script type="application/ld+json"> { "catalogNumber": " minus288 ", "duration": "PT04M41S", "datePublished": "2026-10-29T00:00:00+01:00", "isrcCode": "nl-g3w-26-00092" } </script>`)
	d, err := ParseDetails(page)
	if err != nil || d.Date != "2026-10-29" || d.ISRC != "NLG3W2600092" || d.Catalog != "MINUS288" || d.Duration != 281 {
		t.Errorf("details = %+v err=%v", d, err)
	}
	d, err = ParseDetails([]byte(`"datePublished": "2026-01-02T00:00:00"`))
	if err != nil || d.ISRC != "" || d.Catalog != "" || d.Duration != 0 {
		t.Errorf("a page without an ISRC is fine: %+v %v", d, err)
	}
	if _, err := ParseDetails([]byte("<html></html>")); err == nil {
		t.Error("a page without a date is an error")
	}
}

// fixture mirrors the list view of hardstyle.com: the duration and the label follow the track content.
const fixture = `<html><body><div class="listViewWrapper">
<div class="track listView big" data-track-id="id-1"><div class="trackContent">
 <span class="hoverMarquee"><a class="linkTitle trackTitle" href="/x" title="Flatline"><span class="innerLink">Flatline</span></a></span>
 <span class="hoverMarquee"><a class="linkTitle" href="/x" title="Extended Mix"> Extended Mix </a></span>
 <span class="artists"><a class="highlight" href="/a"><span class="innerLink">Act of Rage <span class="verified"><i class="icon-check"></i></span></span></a>, Spitnoise and Nolz </span>
</div><div class="duration"><i class="icon-track" title="Track"></i><span class="innerDuration">04:41</span></div><div class="label"><a class="link label" href="/en/tracks?label=I AM HARDSTYLE" title="I AM HARDSTYLE">
                                    I AM HARDSTYLE
                                    </a></div><div class="options"><span class="innerLabel">Buy</span></div></div>
<div class="track listView big" data-track-id="id-2"><div class="trackContent">
 <a class="linkTitle trackTitle" href="/y"><span class="innerLink">Drugs (Crypsis Remix)</span></a>
 <span class="artists"><a href="/r"><span class="innerLink">Ran-D</span></a> and <a href="/a"><span class="innerLink">Act of Rage</span></a></span>
</div></div>
<div class="track listView big" data-track-id="id-3"><div class="trackContent"><span class="artists"><a>No title</a></span></div></div>
</div><div class="blockViewWrapper"><div class="track blockView"><a class="trackTitle">Duplicate from block view</a></div></div></body></html>`

func TestParseTrackList(t *testing.T) {
	got, err := parseTrackList(fixture, "Hardstyle")
	if err != nil {
		t.Fatal(err)
	}
	want := []Track{
		{ID: "id-1", Path: "/x", Title: "Flatline", Version: "Extended Mix", Artist: "Act of Rage, Spitnoise and Nolz", Genre: "Hardstyle", Label: "I AM HARDSTYLE", Duration: 281},
		{ID: "id-2", Path: "/y", Title: "Drugs (Crypsis Remix)", Artist: "Ran-D and Act of Rage", Genre: "Hardstyle"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d tracks, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("track %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseClock(t *testing.T) {
	for s, want := range map[string]int{
		"04:41": 281, "0:59": 59, "1:02:03": 3723, "10:00": 600,
		"": 0, "4": 0, "04:75": 0, "a:bc": 0, "1:2:3:4": 0, "-1:30": 0,
	} {
		if got := parseClock(s); got != want {
			t.Errorf("parseClock(%q) = %d, want %d", s, got, want)
		}
	}
}

func TestParseISODuration(t *testing.T) {
	for s, want := range map[string]int{
		"PT4M41S": 281, "PT04M41S": 281, "PT4M6S": 246, "PT1H2M3S": 3723, "PT2H": 7200, "PT45S": 45, "pt3m": 180,
		"PT3M32.5S": 212, "": 0, "PT": 0, "4:41": 0, "P1DT2H": 0, "PT4M41": 0, "garbage": 0,
	} {
		if got := ParseISODuration(s); got != want {
			t.Errorf("ParseISODuration(%q) = %d, want %d", s, got, want)
		}
	}
}

// flakySite answers the first fail requests with status, then with a page, and counts the requests.
func flakySite(t *testing.T, fail int, status int) (*httptest.Server, *atomic.Int32) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("request without User-Agent")
		}
		if int(n.Add(1)) <= fail {
			w.WriteHeader(status)
			return
		}
		fmt.Fprint(w, "page")
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func TestGetRetries(t *testing.T) {
	for _, c := range []struct {
		name         string
		fail, status int
		wantOK       bool
		wantRequests int32
	}{
		{"two server errors, then the page", 2, http.StatusServiceUnavailable, true, 3},
		{"too many requests, then the page", 1, http.StatusTooManyRequests, true, 2},
		{"server errors beyond the retries", 5, http.StatusInternalServerError, false, 3},
		{"not found is not repeated", 5, http.StatusNotFound, false, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, n := flakySite(t, c.fail, c.status)
			body, final, err := Get(context.Background(), srv.Client(), srv.URL+"/p")
			if c.wantOK && (err != nil || string(body) != "page" || final.Path != "/p") {
				t.Errorf("got %q %v %v, want the page", body, final, err)
			}
			var status *StatusError
			if !c.wantOK && (!errors.As(err, &status) || status.Code != c.status) {
				t.Errorf("got %v, want a StatusError with %d", err, c.status)
			}
			if n.Load() != c.wantRequests {
				t.Errorf("%d requests, want %d", n.Load(), c.wantRequests)
			}
		})
	}
}

func TestGetRetriesNetworkErrors(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close() // nobody listens any more: every try is a network error
	var tries atomic.Int32
	client := &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		tries.Add(1)
		return http.DefaultTransport.RoundTrip(r)
	})}
	if _, _, err := Get(context.Background(), client, addr); err == nil {
		t.Fatal("expected an error")
	}
	if tries.Load() != int32(Retries)+1 {
		t.Errorf("%d tries, want %d", tries.Load(), Retries+1)
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGetStopsWhenTheContextEndsDuringThePause(t *testing.T) {
	before := RetryDelay
	RetryDelay = time.Hour
	t.Cleanup(func() { RetryDelay = before })
	srv, n := flakySite(t, 5, http.StatusServiceUnavailable)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	_, _, err := Get(ctx, srv.Client(), srv.URL)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want the context's error", err)
	}
	if n.Load() != 1 {
		t.Errorf("%d requests, want 1", n.Load())
	}
}

func TestParseTrackListPastLastPage(t *testing.T) {
	_, err := parseTrackList("<html><body><p>No results found, please try another selection.</p></body></html>", "Uptempo")
	if !errors.Is(err, ErrNoResults) {
		t.Fatalf("got %v, want ErrNoResults", err)
	}
}

func TestParseTrackListSiteChanged(t *testing.T) {
	if _, err := parseTrackList("<html><body>nothing</body></html>", "Hardstyle"); err == nil {
		t.Fatal("expected an error when the track list is missing")
	}
}

// Run with HS_LIVE=1 to check the parser against the real site.
func TestLiveSite(t *testing.T) {
	if os.Getenv("HS_LIVE") == "" {
		t.Skip("set HS_LIVE=1 to hit hardstyle.com")
	}
	for _, genre := range []string{"Hardstyle", "Hardcore", AnyGenre} {
		tracks, err := FetchReleases(context.Background(), http.DefaultClient, genre, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(tracks) != 60 {
			t.Errorf("%s: got %d tracks, want 60", genre, len(tracks))
		}
		withDuration, withLabel := 0, 0
		for _, tr := range tracks {
			if tr.Duration > 0 {
				withDuration++
			}
			if tr.Label != "" {
				withLabel++
			}
		}
		t.Logf("%s: %d of %d tracks with a duration, %d with a label", GenreLabel(genre), withDuration, len(tracks), withLabel)
		for _, tr := range tracks[:3] {
			t.Logf("%s: %s | %s | %s %ds (%s)", genre, tr.Title, tr.Artist, tr.Label, tr.Duration, tr.Path)
		}
		if genre == "Hardstyle" {
			f := NewDetailFetcher(http.DefaultClient, time.Second)
			withDuration := 0
			for _, tr := range tracks[:5] {
				d, err := f.Fetch(context.Background(), tr)
				if err != nil || d.Date == "" {
					t.Fatalf("details of %q: %+v %v", tr.Title, d, err)
				}
				if d.Duration > 0 {
					withDuration++
				}
				t.Logf("details of %q: %+v (list: %ds)", tr.Title, d, tr.Duration)
			}
			t.Logf("release pages: %d of 5 with a duration", withDuration)
		}
	}
}

func TestDatePublishedIsFoundInThePage(t *testing.T) {
	page := `<script type="application/ld+json"> { "@type": "MusicRelease", "datePublished": "2026-10-29T00:00:00+01:00", "dateModified": "2026-10-28T12:15:17+01:00" } </script>`
	if m := datePublished.FindStringSubmatch(page); m == nil || m[1] != "2026-10-29" {
		t.Errorf("match = %v", m)
	}
}

func TestTrackListURL(t *testing.T) {
	if got := trackListURL(AnyGenre, 3); got != "https://hardstyle.com/en/tracks?page=3" {
		t.Errorf("general list: %s", got)
	}
	if got := trackListURL("UK/Happy Hardcore", 1); got != "https://hardstyle.com/en/tracks?genre=UK%2FHappy+Hardcore&page=1" {
		t.Errorf("genre: %s", got)
	}
}

// listOnlySite is a small hardstyle.com that serves the list pages and counts the requests.
type listOnlySite struct {
	mu    sync.Mutex
	hits  map[string]int
	pages int
}

func (s *listOnlySite) count(what string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[what]
}

func (s *listOnlySite) start(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.hits == nil {
			s.hits = map[string]int{}
		}
		s.hits["list"]++
		page := 0
		fmt.Sscan(r.URL.Query().Get("page"), &page)
		if page > s.pages {
			fmt.Fprint(w, "<html><body>No results found, please try another selection.</body></html>")
			return
		}
		var b strings.Builder
		b.WriteString(`<html><body><div class="listViewWrapper">`)
		for i := (page-1)*3 + 1; i <= min(page*3, s.pages*3); i++ {
			fmt.Fprintf(&b, `<div class="track listView" data-track-id="id%d"><a class="linkTitle trackTitle" href="/en/tracks/id%d/x"><span class="innerLink">T%d</span></a><span class="artists"><a><span class="innerLink">A</span></a></span></div>`, i, i, i)
		}
		b.WriteString(`</div></body></html>`)
		fmt.Fprint(w, b.String())
	}))
	t.Cleanup(srv.Close)
	before := Base
	Base = srv.URL
	t.Cleanup(func() { Base = before })
	return srv
}

func TestFetchReleasesReadsAtMostPagesAndStopsAtTheEnd(t *testing.T) {
	site := &listOnlySite{pages: 3}
	srv := site.start(t)
	for _, c := range []struct {
		pages, wantTracks, wantRequests int
	}{
		{2, 6, 2}, // the setting ends it
		{5, 9, 4}, // the list ends it: three pages and the one past the end
	} {
		site.hits = nil
		got, err := FetchReleases(context.Background(), srv.Client(), AnyGenre, c.pages)
		if err != nil || len(got) != c.wantTracks || site.count("list") != c.wantRequests {
			t.Errorf("pages=%d: %d tracks, %d requests (err %v), want %d and %d", c.pages, len(got), site.count("list"), err, c.wantTracks, c.wantRequests)
		}
	}
}
