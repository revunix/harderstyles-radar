package sync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	gosync "sync"
	"testing"

	"hardstylesync/internal/config"
	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/hdrr"
)

// fakeHdrr is a small hdrr.info: the Hardstyle release radar with a track hardstyle.com also has (in the
// Spotify spelling "T1 - Extended Mix") and one of its own, and a page for every track. It counts the requests
// per path.
type fakeHdrr struct {
	mu   gosync.Mutex
	hits map[string]int
}

func (s *fakeHdrr) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

func (s *fakeHdrr) start(t *testing.T) {
	s.hits = map[string]int{}
	li := func(slug, title string) string {
		return fmt.Sprintf(`<li data-position="1"><div><a href="/tracks/%s">%s</a><p><a href="/artists/a">A</a></p></div><time datetime="2026-10-29T00:00:00.000Z">29 Oct 2026</time></li>`, slug, title)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits[r.URL.Path]++
		s.mu.Unlock()
		switch {
		case r.URL.Path == "/playlists/hardstyle-release-radar":
			fmt.Fprint(w, `<html><body><ol data-list="playlist">`+li("t6", "T6")+li("t1", "T1 - Extended Mix")+`</ol></body></html>`)
		case strings.HasPrefix(r.URL.Path, "/tracks/"):
			fmt.Fprint(w, `<script type="application/ld+json">{"datePublished":"2026-10-29","duration":"PT4M0S"}</script>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	before, detail := hdrr.Base, hdrr.DetailDelay
	hdrr.Base, hdrr.DetailDelay = srv.URL, 0
	t.Cleanup(func() { hdrr.Base, hdrr.DetailDelay = before, detail })
}

func TestRunTakesHdrrReleasesTheShopsLack(t *testing.T) {
	site := &fakeSite{tracks: []hardstyle.Track{
		{ID: "1", Title: "T1", Version: "Extended Mix", Artist: "A"},
		{ID: "2", Title: "T2", Artist: "A"},
	}}
	srv := site.start(t)
	h := &fakeHdrr{}
	h.start(t)
	f := &fakeSpotify{}
	sp := newFakeClient(t, f)
	sp.SetDelay(0)
	_, path := newCache(t)
	cfg := config.Config{PlaylistID: "p", Genres: []string{"Hardstyle"}, HdrrPlaylists: []string{"hardstyle"}, Pages: 3,
		MaxTracks: 500, MaxSearches: 100, DateTolerance: 14, CacheFile: path, Description: descriptionOff}

	res, err := Run(context.Background(), srv.Client(), sp, cfg, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := f.titles()
	slices.Sort(got)
	if want := []string{"T1", "T2", "T6"}; !slices.Equal(got, want) {
		t.Fatalf("playlist = %v, want %v: the shop's releases and the one only hdrr.info has, each once", got, want)
	}
	if res.Scraped != 3 {
		t.Errorf("scraped = %d, want 3 releases after removing the duplicate", res.Scraped)
	}
	if n := h.count("/tracks/t1"); n != 0 {
		t.Errorf("the duplicate's hdrr.info page was read %d times, it comes from hardstyle.com", n)
	}
	if n := h.count("/tracks/t6"); n != 1 {
		t.Errorf("/tracks/t6 read %d times, want once for the ISRC", n)
	}
}

func TestWindowPrefersTheShopsOverHdrr(t *testing.T) {
	hd := hardstyle.Track{Title: "Song", Version: "Extended Mix", Artist: "A", Path: "https://www.hdrr.info/tracks/song", ID: hdrr.IDPrefix + "song"}
	ht := hardstyle.Track{Title: "Song", Version: "Extended", Artist: "A", Path: "https://www.hardtunes.com/tunes/song/1", ID: "2026-10-25"}
	dates := func(_ context.Context, t hardstyle.Track) string {
		if t.ID == ht.ID {
			return t.ID
		}
		return "2026-10-26"
	}
	feeds := []*feed{pagedFeed("hdrr.info", []hardstyle.Track{hd}), pagedFeed("hardtunes.com", []hardstyle.Track{ht})}
	got, _ := newestReleases(context.Background(), feeds, windowOptions{limit: 10, dateOf: dates})
	if len(got) != 1 || got[0].Path != ht.Path {
		t.Errorf("got %+v, want the release once, from hardtunes.com", got)
	}
	if sourceRank(hardstyle.Track{Path: "/en/tracks/1"}) != 0 || sourceRank(ht) != 1 || sourceRank(hd) != 2 {
		t.Error("ranks: hardstyle.com 0, hardtunes.com 1, hdrr.info 2")
	}
}
