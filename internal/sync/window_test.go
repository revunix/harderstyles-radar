package sync

import (
	"context"
	"errors"
	"slices"
	"testing"

	"hardstylesync/internal/config"
	"hardstylesync/internal/hardstyle"
)

// pagedFeed is a list of the given pages; the test puts a release's date into its ID. It counts the pages read.
func pagedFeed(name string, pages ...[]hardstyle.Track) *feed {
	return &feed{name: name, maxPages: 100, fetch: func(_ context.Context, page int) ([]hardstyle.Track, error) {
		if page > len(pages) {
			return nil, errEndOfList
		}
		return pages[page-1], nil
	}}
}

func rel(title, date string) hardstyle.Track {
	return hardstyle.Track{Title: title, Artist: "A", Path: "/en/tracks/" + title, ID: date}
}

// dateByID reads the date the test put into the ID.
func dateByID(_ context.Context, t hardstyle.Track) string { return t.ID }

// limitOf is a window of n releases dated by dateByID.
func limitOf(n int) windowOptions { return windowOptions{limit: n, dateOf: dateByID} }

func TestWindowGoesOnWithoutAListThatFails(t *testing.T) {
	broken := &feed{name: "broken", maxPages: 5, fetch: func(context.Context, int) ([]hardstyle.Track, error) {
		return nil, errors.New("HTTP 500")
	}}
	good := pagedFeed("good", []hardstyle.Track{rel("g1", "2026-10-30")})
	got, err := newestReleases(context.Background(), []*feed{broken, good}, limitOf(10))
	if err != nil || !slices.Equal(titlesOf(got), []string{"g1"}) || broken.err == nil {
		t.Errorf("got %v, err %v, broken.err %v: the good list must be read, the broken one marked", titlesOf(got), err, broken.err)
	}
	if _, err := newestReleases(context.Background(), []*feed{broken}, limitOf(10)); err == nil {
		t.Error("when no list can be read the window must fail")
	}
}

func TestGenreMinimumKeepsSmallGenres(t *testing.T) {
	feeds := func() []*feed {
		busy := pagedFeed("busy", []hardstyle.Track{rel("b1", "2026-10-30"), rel("b2", "2026-10-29"), rel("b3", "2026-10-28")})
		busy.genre = "Hardstyle"
		small := pagedFeed("small", []hardstyle.Track{rel("s1", "2026-09-01"), rel("s2", "2026-08-01")})
		small.genre = "Terror"
		return []*feed{busy, small}
	}
	o := limitOf(3)
	if got, _ := newestReleases(context.Background(), feeds(), o); !slices.Equal(titlesOf(got), []string{"b1", "b2", "b3"}) {
		t.Errorf("by date only: %v", titlesOf(got))
	}
	o.minPerGenre = 1
	// Each genre first gets its newest one, the rest goes by date; the window stays sorted by date.
	if got, _ := newestReleases(context.Background(), feeds(), o); !slices.Equal(titlesOf(got), []string{"b1", "b2", "s1"}) {
		t.Errorf("with a minimum of 1: %v", titlesOf(got))
	}
}

func TestWindowTakesAReleaseOnceByISRCOrVersionSpelling(t *testing.T) {
	hs := hardstyle.Track{Title: "Song", Version: "Extended Mix", Artist: "A", Path: "/en/tracks/song", ID: "2026-10-20"}
	ht := hardstyle.Track{Title: "Song", Version: "Extended", Artist: "A", Path: "https://www.hardtunes.com/tunes/song/1", ID: "2026-10-25"}
	other := hardstyle.Track{Title: "Different Name", Artist: "B", Path: "https://www.hardtunes.com/tunes/x/2", ID: "2026-10-24"}
	same := hardstyle.Track{Title: "Same Recording", Artist: "B b", Path: "/en/tracks/same", ID: "2026-10-19"}
	o := limitOf(10)
	o.isrcOf = func(t hardstyle.Track) string {
		if t.Title == "Different Name" || t.Title == "Same Recording" {
			return "NL1234567890"
		}
		return ""
	}
	feeds := []*feed{pagedFeed("hardstyle.com", []hardstyle.Track{hs, same}), pagedFeed("hardtunes.com", []hardstyle.Track{ht, other})}
	got, _ := newestReleases(context.Background(), feeds, o)
	if len(got) != 2 || got[0].Path != hs.Path || got[1].Path != same.Path {
		t.Errorf("got %+v, want each release once, from hardstyle.com", got)
	}
	if releaseKey(hardstyle.Track{Title: "Song", Version: "Radio Edit", Artist: "A"}) == releaseKey(hs) {
		t.Error("a radio edit is another release than the extended mix")
	}
}

func titlesOf(ts []hardstyle.Track) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Title)
	}
	return out
}

func TestWindowTakesTheNewestOfAllListsAndReadsNoPageMore(t *testing.T) {
	a := []*feed{
		pagedFeed("a", []hardstyle.Track{rel("a1", "2026-10-30"), rel("a2", "2026-10-28"), rel("a3", "2026-10-26")},
			[]hardstyle.Track{rel("a4", "2026-10-24")}),
		pagedFeed("b", []hardstyle.Track{rel("b1", "2026-10-29"), rel("b2", "2026-10-27"), rel("b3", "2026-10-25")},
			[]hardstyle.Track{rel("b4", "2026-10-23")}),
	}
	got, err := newestReleases(context.Background(), a, limitOf(4))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a1", "b1", "a2", "b2"}; !slices.Equal(titlesOf(got), want) {
		t.Errorf("limit 4: %v, want %v", titlesOf(got), want)
	}
	if a[0].page != 1 || a[1].page != 1 {
		t.Errorf("the first pages hold the newest 4, but %d and %d pages were read", a[0].page, a[1].page)
	}

	// A larger limit reaches into the second pages, and a list that ends is no error.
	b := []*feed{
		pagedFeed("a", []hardstyle.Track{rel("a1", "2026-10-30"), rel("a2", "2026-10-28"), rel("a3", "2026-10-26")},
			[]hardstyle.Track{rel("a4", "2026-10-24")}),
		pagedFeed("b", []hardstyle.Track{rel("b1", "2026-10-29"), rel("b2", "2026-10-27"), rel("b3", "2026-10-25")},
			[]hardstyle.Track{rel("b4", "2026-10-23")}),
	}
	got, _ = newestReleases(context.Background(), b, limitOf(100))
	if want := []string{"a1", "b1", "a2", "b2", "a3", "b3", "a4", "b4"}; !slices.Equal(titlesOf(got), want) {
		t.Errorf("everything: %v, want %v", titlesOf(got), want)
	}
}

func TestWindowTakesTheHardstyleComCopyOfAReleaseOnBothSites(t *testing.T) {
	hs := rel("Song", "2026-10-20")
	ht := hardstyle.Track{Title: "Song", Artist: "A", Path: "https://www.hardtunes.com/tunes/song/1", ID: "2026-10-25"}
	feeds := []*feed{pagedFeed("hardstyle.com", []hardstyle.Track{hs}), pagedFeed("hardtunes.com", []hardstyle.Track{ht})}
	got, _ := newestReleases(context.Background(), feeds, limitOf(10))
	if len(got) != 1 || got[0].Path != hs.Path {
		t.Errorf("got %+v, want the release once, from hardstyle.com", got)
	}
}

func TestWindowGivesAReleaseWithoutDateTheDateBeforeIt(t *testing.T) {
	a := pagedFeed("a", []hardstyle.Track{rel("first", ""), rel("a2", "2026-10-28"), rel("nodate", ""), rel("a4", "2026-10-20")})
	b := pagedFeed("b", []hardstyle.Track{rel("b1", "2026-10-29"), rel("b2", "2026-10-24")})
	got, _ := newestReleases(context.Background(), []*feed{a, b}, limitOf(100))
	// "first" counts as newest of all, "nodate" as old as a2 (before b2 of 10-24).
	if want := []string{"first", "b1", "a2", "nodate", "b2", "a4"}; !slices.Equal(titlesOf(got), want) {
		t.Errorf("got %v, want %v", titlesOf(got), want)
	}
}

func TestQuickWindowStopsAfterThePageWithAKnownRelease(t *testing.T) {
	f := pagedFeed("a", []hardstyle.Track{rel("new", "2026-10-30"), rel("known", "2026-10-29")},
		[]hardstyle.Track{rel("older", "2026-10-28")})
	f.stopAt = func(t hardstyle.Track) bool { return t.Title == "known" }
	got, _ := newestReleases(context.Background(), []*feed{f}, limitOf(100))
	if !slices.Equal(titlesOf(got), []string{"new", "known"}) || f.page != 1 {
		t.Errorf("got %v after %d pages, want the first page only", titlesOf(got), f.page)
	}
}

func TestRunSearchesOnlyTheNewestMaxTracks(t *testing.T) {
	site := &fakeSite{tracks: siteTracks(9)} // three pages of three
	srv := site.start(t)
	f := &fakeSpotify{}
	sp := newFakeClient(t, f)
	sp.SetDelay(0)
	_, path := newCache(t)
	cfg := config.Config{PlaylistID: "p", Genres: []string{hardstyle.AnyGenre}, Pages: 10, MaxTracks: 4,
		MaxSearches: 100, DateTolerance: 14, CacheFile: path, Description: descriptionOff, Order: config.OrderNewest}

	res, err := Run(context.Background(), srv.Client(), sp, cfg, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	searches, _, _, _ := f.state()
	if res.Scraped != 4 || searches != 4 || len(f.titles()) != 4 {
		t.Errorf("scraped=%d searches=%d playlist=%v, want 4 of each", res.Scraped, searches, f.titles())
	}
	if site.count("list") != 2 || site.count("detail") != 4 {
		t.Errorf("%d list pages and %d release pages read, want 2 and 4: no page more than the 4 newest need",
			site.count("list"), site.count("detail"))
	}
}

func TestRunReadsOnUntilTheTracksFillMaxTracks(t *testing.T) {
	site := &fakeSite{tracks: siteTracks(9)}
	srv := site.start(t)
	f := &fakeSpotify{missing: map[string]bool{"T2": true}}
	sp := newFakeClient(t, f)
	sp.SetDelay(0)
	_, path := newCache(t)
	cfg := config.Config{PlaylistID: "p", Genres: []string{hardstyle.AnyGenre}, Pages: 10, MaxTracks: 4,
		MaxSearches: 100, DateTolerance: 14, CacheFile: path, Description: descriptionOff, Order: config.OrderNewest}

	for range 2 {
		if _, err := Run(context.Background(), srv.Client(), sp, cfg, false, false, nil); err != nil {
			t.Fatal(err)
		}
	}
	// The first run learns that Spotify lacks T2; the second takes one release more to make up for it.
	if want := []string{"T1", "T3", "T4", "T5"}; !slices.Equal(f.titles(), want) {
		t.Errorf("playlist %v, want %v", f.titles(), want)
	}
}

func TestCheckForNewTracksBuildsThePlaylistFromTheWholeWindow(t *testing.T) {
	dates := map[string]string{"N": "2026-10-31", "B1": "2026-10-30", "B2": "2026-10-30", "B3": "2026-10-29",
		"B4": "2026-10-28", "B5": "2026-10-27", "S1": "2026-09-01", "S2": "2026-08-01"}
	list := func(titles ...string) []hardstyle.Track {
		var out []hardstyle.Track
		for _, title := range titles {
			out = append(out, hardstyle.Track{ID: "id" + title, Title: title, Artist: "A"})
		}
		return out
	}
	site := &fakeSite{
		genres: map[string][]hardstyle.Track{"Busy": list("B1", "B2", "B3", "B4", "B5"), "Slow": list("S1", "S2")},
		dates:  map[string]hardstyle.Details{},
	}
	for title, d := range dates {
		site.dates["id"+title] = hardstyle.Details{Date: d}
	}
	srv := site.start(t)
	f := &fakeSpotify{dates: dates}
	sp := newFakeClient(t, f)
	sp.SetDelay(0)
	_, path := newCache(t)
	cfg := config.Config{PlaylistID: "p", Genres: []string{"Busy", "Slow"}, Pages: 10, MaxTracks: 4,
		MaxSearches: 100, DateTolerance: 14, CacheFile: path, Description: descriptionOff, Order: config.OrderNewest}

	if _, err := Run(context.Background(), srv.Client(), sp, cfg, false, false, nil); err != nil {
		t.Fatal(err)
	}
	if want := []string{"B1", "B2", "B3", "B4"}; !slices.Equal(f.titles(), want) {
		t.Fatalf("full run: playlist %v, want %v", f.titles(), want)
	}

	// A new release on the busy list. The check reads its first page only, and all of the quiet list, whose
	// releases are much older; the playlist must still hold the newest four, not the old S1 instead of B3.
	site.mu.Lock()
	site.genres["Busy"] = list("N", "B1", "B2", "B3", "B4", "B5")
	site.mu.Unlock()
	if _, err := Run(context.Background(), srv.Client(), sp, cfg, false, true, nil); err != nil {
		t.Fatal(err)
	}
	if want := []string{"N", "B1", "B2", "B3"}; !slices.Equal(f.titles(), want) {
		t.Errorf("after the check: playlist %v, want %v", f.titles(), want)
	}
}
