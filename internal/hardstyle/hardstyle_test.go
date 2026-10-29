package hardstyle

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
)

func init() { PageDelay, DetailDelay = 0, 0 }

func TestParseDetails(t *testing.T) {
	page := []byte(`<script type="application/ld+json"> { "catalogNumber": " minus288 ", "datePublished": "2026-10-29T00:00:00+01:00", "isrcCode": "nl-g3w-26-00092" } </script>`)
	d, err := parseDetails(page)
	if err != nil || d.Date != "2026-10-29" || d.ISRC != "NLG3W2600092" || d.Catalog != "MINUS288" {
		t.Errorf("details = %+v err=%v", d, err)
	}
	d, err = parseDetails([]byte(`"datePublished": "2026-01-02T00:00:00"`))
	if err != nil || d.ISRC != "" || d.Catalog != "" {
		t.Errorf("a page without an ISRC is fine: %+v %v", d, err)
	}
	if _, err := parseDetails([]byte("<html></html>")); err == nil {
		t.Error("a page without a date is an error")
	}
}

const fixture = `<html><body><div class="listViewWrapper">
<div class="track listView big" data-track-id="id-1"><div class="trackContent">
 <span class="hoverMarquee"><a class="linkTitle trackTitle" href="/x" title="Flatline"><span class="innerLink">Flatline</span></a></span>
 <span class="hoverMarquee"><a class="linkTitle" href="/x" title="Extended Mix"> Extended Mix </a></span>
 <span class="artists"><a class="highlight" href="/a"><span class="innerLink">Act of Rage <span class="verified"><i class="icon-check"></i></span></span></a>, Spitnoise and Nolz </span>
</div></div>
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
		{ID: "id-1", Path: "/x", Title: "Flatline", Version: "Extended Mix", Artist: "Act of Rage, Spitnoise and Nolz", Genre: "Hardstyle"},
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
		for _, tr := range tracks[:3] {
			t.Logf("%s: %s | %s (%s)", genre, tr.Title, tr.Artist, tr.Path)
		}
		if genre == "Hardstyle" {
			d, err := NewDetailFetcher(http.DefaultClient, 0).Fetch(context.Background(), tracks[0])
			if err != nil || d.Date == "" {
				t.Fatalf("details of %q: %+v %v", tracks[0].Title, d, err)
			}
			t.Logf("details of %q: %+v", tracks[0].Title, d)
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

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestFetchUntilKnownStopsAtTheFirstKnownPage(t *testing.T) {
	site := &listOnlySite{pages: 3}
	srv := site.start(t)
	known := func(ids ...string) func(Track) bool {
		return func(t Track) bool { return slices.Contains(ids, t.ID) }
	}
	for _, c := range []struct {
		name                  string
		known                 func(Track) bool
		wantTracks, wantPages int
	}{
		{"known on the first page", known("id2"), 3, 1},
		{"known on the second page", known("id5"), 6, 2},
		{"nothing known: all pages the setting allows", known(), 9, 3},
		{"nil: all pages", nil, 9, 3},
	} {
		site.hits = nil
		got, err := FetchUntilKnown(context.Background(), srv.Client(), AnyGenre, 5, c.known)
		if err != nil || len(got) != c.wantTracks || site.count("list") != c.wantPages+boolInt(c.wantPages == 3) {
			t.Errorf("%s: %d tracks, %d list requests (err %v), want %d tracks and %d pages", c.name, len(got), site.count("list"), err, c.wantTracks, c.wantPages)
		}
	}
	site.hits = nil
	if got, _ := FetchUntilKnown(context.Background(), srv.Client(), AnyGenre, 2, nil); len(got) != 6 {
		t.Errorf("max 2 pages: %d tracks", len(got))
	}
}
