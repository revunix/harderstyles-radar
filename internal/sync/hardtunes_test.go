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
	"hardstylesync/internal/hardtunes"
)

// fakeHardtunes is a small hardtunes.com: one page of the Hardstyle list with a tune that hardstyle.com also
// has, a tune of its own and an album, the album page with its tracklist and a panel of other releases, and a
// page for every tune. It counts the requests per path.
type fakeHardtunes struct {
	mu   gosync.Mutex
	hits map[string]int
}

func (s *fakeHardtunes) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

func htItem(base, path, title, typ, date string) string {
	return fmt.Sprintf(`<div class="col-xs-6 release-list-item"><div class="release-list-item-info"><div class="release-list-item-info-primary"><p class="release-list-item-title"><a href="%s%s">%s</a></p><p class="release-list-item-artist"><a href="%s/artists/a">A</a></p></div><div class="release-list-item-info-secondary"><p class="release-list-item-type"><a href="%s%s">%s</a></p><p class="release-list-item-release-date">%s</p></div></div></div>`,
		base, path, title, base, base, path, typ, date)
}

func (s *fakeHardtunes) start(t *testing.T) *httptest.Server {
	s.hits = map[string]int{}
	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits[r.URL.Path]++
		s.mu.Unlock()
		switch {
		case r.URL.Path == "/hardstyle/page/1/":
			fmt.Fprint(w, `<html><body><div class="row release-list release-list-normal">`+
				htItem(base, "/albums/bundle/900", "Bundle", "2 Tunes", "28.10.2026")+
				htItem(base, "/tunes/t4/104", "T4 (Extended Mix)", "Single tune", "28.10.2026")+
				htItem(base, "/tunes/t1/101", "T1 (Extended Mix)", "Single tune", "28.10.2026")+
				`</div></body></html>`)
		case strings.HasPrefix(r.URL.Path, "/hardstyle/page/"):
			http.Redirect(w, r, "/hardstyle/page/1/", http.StatusFound) // past the last page
		case r.URL.Path == "/albums/bundle/900":
			fmt.Fprint(w, `<html><body>`+
				`<div class="panel panel-default"><div class="panel-heading">Tracklist</div><div class="panel-body"><div class="row release-list">`+
				htItem(base, "/tunes/t4/104", "T4 (Extended Mix)", "Single tune", "28.10.2026")+
				htItem(base, "/tunes/t5/105", "T5", "Single tune", "28.10.2026")+
				`</div></div></div>`+
				`<div class="panel panel-default"><div class="panel-heading">People who bought this also bought</div><div class="panel-body"><div class="row release-list">`+
				htItem(base, "/tunes/t9/109", "T9", "Single tune", "01.01.2020")+
				`</div></div></div></body></html>`)
		case strings.HasPrefix(r.URL.Path, "/tunes/"):
			fmt.Fprint(w, `<html><body><meta itemprop="name" content="x"></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	base = srv.URL
	t.Cleanup(srv.Close)
	before, page, detail := hardtunes.Base, hardtunes.PageDelay, hardtunes.DetailDelay
	hardtunes.Base, hardtunes.PageDelay, hardtunes.DetailDelay = srv.URL, 0, 0
	t.Cleanup(func() { hardtunes.Base, hardtunes.PageDelay, hardtunes.DetailDelay = before, page, detail })
	return srv
}

func TestRunTakesHardtunesReleasesHardstyleComLacks(t *testing.T) {
	site := &fakeSite{tracks: []hardstyle.Track{
		{ID: "1", Title: "T1", Version: "Extended Mix", Artist: "A"},
		{ID: "2", Title: "T2", Artist: "A"},
		{ID: "3", Title: "T3", Artist: "A"},
	}}
	srv := site.start(t)
	ht := &fakeHardtunes{}
	ht.start(t)
	f := &fakeSpotify{}
	sp := newFakeClient(t, f)
	sp.SetDelay(0)
	_, path := newCache(t)
	cfg := config.Config{PlaylistID: "p", Genres: []string{"Hardstyle"}, HardtunesGenres: []string{"hardstyle"}, Pages: 3,
		MaxTracks: 500, MaxSearches: 100, DateTolerance: 14, CacheFile: path, Description: descriptionOff}

	res, err := Run(context.Background(), srv.Client(), sp, cfg, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := f.titles()
	slices.Sort(got)
	if want := []string{"T1", "T2", "T3", "T4", "T5"}; !slices.Equal(got, want) {
		t.Fatalf("playlist = %v, want %v: both sites, the album's tunes, every release once", got, want)
	}
	if res.Scraped != 5 {
		t.Errorf("scraped = %d, want 5 releases after removing the duplicate", res.Scraped)
	}
	// T1 is on both sites: it comes from hardstyle.com, so its hardtunes.com page is never read.
	if n := ht.count("/tunes/t1/101"); n != 0 {
		t.Errorf("the duplicate's hardtunes.com page was read %d times", n)
	}
	for _, p := range []string{"/tunes/t4/104", "/tunes/t5/105"} {
		if n := ht.count(p); n != 1 {
			t.Errorf("%s read %d times, want once for the ISRC", p, n)
		}
	}
	if n := ht.count("/tunes/t9/109"); n != 0 {
		t.Errorf("a release of the 'also bought' panel was taken (%d reads)", n)
	}
}

func TestReleaseKeyIgnoresArtistOrderButKeepsVersions(t *testing.T) {
	a := releaseKey(hardstyle.Track{Artist: "Dredd & Striker", Title: "Rebels of Insurgency", Version: "Extended Mix"})
	b := releaseKey(hardstyle.Track{Artist: "Striker and Dredd", Title: "Rebels Of Insurgency", Version: "Extended Mix"})
	if a != b {
		t.Errorf("same release, different keys: %q vs %q", a, b)
	}
	if c := releaseKey(hardstyle.Track{Artist: "Dredd & Striker", Title: "Rebels of Insurgency", Version: "Radio Edit"}); c == a {
		t.Errorf("a radio edit must stay a release of its own")
	}
}
