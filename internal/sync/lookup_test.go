package sync

import (
	"context"
	"fmt"
	"hardstylesync/internal/config"
	"hardstylesync/internal/event"
	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/i18n"
	"hardstylesync/internal/spotify"
	"slices"
	"strings"
	"testing"
)

func TestSpecialLettersAreFoundOnSpotify(t *testing.T) {
	f := &fakeSpotify{catalog: []fakeTrack{
		{URI: "spotify:track:LC", Name: "Lose Control", Artists: []string{"Juliëx"}, Date: "2026-10-23", Key: "lose control"},
		{URI: "spotify:track:LCX", Name: "Lose Control", Artists: []string{"Noelis"}, Date: "2026-07-21", Key: "lose control"},
		{URI: "spotify:track:HC", Name: "HARDCORE", Artists: []string{"GRIVØ"}, Date: "2026-10-23", Key: "hardcore"},
		{URI: "spotify:track:HCX", Name: "HARDCORE SOUND", Artists: []string{"Shogun"}, Date: "2026-10-16", Key: "hardcore"},
	}}
	sp := newFakeClient(t, f)
	c, _ := newCache(t)
	rel := []hardstyle.Track{
		{ID: "a", Title: "Lose Control", Artist: "Juliex", Genre: ""},
		{ID: "b", Title: "HARDCORE", Artist: "GRIVO"},
	}
	o := opts(nil)
	o.FetchDetails = detailsOf(map[string]hardstyle.Details{"Lose Control": {Date: "2026-10-23"}, "HARDCORE": {Date: "2026-10-23"}})
	res := mustSync(t, sp, rel, o, c)
	if got := f.titles(); !slices.Equal(got, []string{"LC", "HC"}) || len(res.Unmatched) != 0 {
		t.Errorf("playlist = %v unmatched = %v, want Lose Control by Juliëx and HARDCORE by GRIVØ", got, res.Unmatched)
	}
}

// hardstyle.com writes the artist line with "and" and the title without the article, Spotify both differently:
// the search found nothing and the title did not fit. "Gabber I'm G" by "Double Trouble and Dedicated" is
// "Gabber I'm A G" by "Double Trouble" and "Dedicated" there.
func TestAnArtistLineWithAndAndATitleWithAnArticle(t *testing.T) {
	f := &fakeSpotify{catalog: []fakeTrack{
		{URI: "spotify:track:G", Name: "Gabber I'm A G", Artists: []string{"Double Trouble", "Dedicated"}, Date: "2026-10-16",
			Key: "gabber i'm g double trouble dedicated"},
	}}
	sp := newFakeClient(t, f)
	c, _ := newCache(t)
	rel := []hardstyle.Track{{ID: "a", Title: "Gabber I'm G", Artist: "Double Trouble and Dedicated"}}
	res := mustSync(t, sp, rel, opts(map[string]string{"Gabber I'm G": "2026-10-29"}), c)
	if got := f.titles(); !slices.Equal(got, []string{"G"}) || len(res.Unmatched) != 0 {
		t.Fatalf("playlist = %v unmatched = %v, want Gabber I'm A G", got, res.Unmatched)
	}
	if searches, _, _, _ := f.state(); searches != 1 {
		t.Errorf("%d searches, the first query must find it", searches)
	}
}

func TestVersionsOfTheSameSongAllGoIn(t *testing.T) {
	catalog := []fakeTrack{
		{URI: "spotify:track:FM", Name: "Everything Is Looking Good - Fiesta Mix", Artists: []string{"Jose Del Barrio"}, Date: "2026-10-26", ISRC: "GBLV62617451", Key: "everything is looking good"},
		{URI: "spotify:track:HM", Name: "Everything Is Looking Good - Happy Mix", Artists: []string{"Jose Del Barrio"}, Date: "2026-10-26", ISRC: "GBLV62617450", Key: "everything is looking good"},
	}
	rel := []hardstyle.Track{
		{ID: "a", Title: "Everything Is Looking Good", Version: "Happy Mix", Artist: "Jose Del Barrio"},
		{ID: "b", Title: "Everything Is Looking Good", Version: "Fiesta Mix", Artist: "Jose Del Barrio"},
	}
	for _, withISRC := range []bool{false, true} {
		f := &fakeSpotify{catalog: catalog}
		sp := newFakeClient(t, f)
		c, _ := newCache(t)
		o := opts(nil)
		m := map[string]hardstyle.Details{"Everything Is Looking Good|Happy Mix": {Date: "2026-10-26"}, "Everything Is Looking Good|Fiesta Mix": {Date: "2026-10-26"}}
		if withISRC {
			m["Everything Is Looking Good|Happy Mix"] = hardstyle.Details{Date: "2026-10-26", ISRC: "GBLV62617450"}
			m["Everything Is Looking Good|Fiesta Mix"] = hardstyle.Details{Date: "2026-10-26", ISRC: "GBLV62617451"}
		}
		o.FetchDetails = detailsOf(m)
		mustSync(t, sp, rel, o, c)
		if got := f.titles(); !slices.Equal(got, []string{"HM", "FM"}) {
			t.Errorf("withISRC=%v: playlist = %v, want both versions, each once", withISRC, got)
		}
		if searches, _, _, _ := f.state(); withISRC && searches != 2 {
			t.Errorf("with the ISRC one search per release is enough, got %d", searches)
		}
	}
}

func TestISRCFindsWhatTheNamesCannot(t *testing.T) {
	f := &fakeSpotify{catalog: []fakeTrack{
		{URI: "spotify:track:X", Name: "Totally Different Name", Artists: []string{"Other Artist"}, Date: "2020-01-01", ISRC: "NLABC2600001", Key: "never matched by text"},
	}}
	sp := newFakeClient(t, f)
	c, _ := newCache(t)
	o := opts(nil)
	o.FetchDetails = detailsOf(map[string]hardstyle.Details{"Song": {Date: "2026-10-29", ISRC: "NLABC2600001"}})
	mustSync(t, sp, []hardstyle.Track{{ID: "a", Title: "Song", Artist: "Artist"}}, o, c)
	if got := f.titles(); !slices.Equal(got, []string{"X"}) {
		t.Fatalf("playlist = %v: the ISRC names exactly one recording, whatever the names are", got)
	}
	if searches, _, _, _ := f.state(); searches != 1 {
		t.Errorf("%d searches, one is enough with an ISRC", searches)
	}

	// An ISRC Spotify does not know: the search by names takes over.
	f2 := &fakeSpotify{catalog: []fakeTrack{{URI: "spotify:track:S", Name: "Song", Artists: []string{"Artist"}, Date: "2026-10-29", ISRC: "OTHER", Key: "song"}}}
	sp2 := newFakeClient(t, f2)
	c2, _ := newCache(t)
	mustSync(t, sp2, []hardstyle.Track{{ID: "a", Title: "Song", Artist: "Artist"}}, o, c2)
	if got := f2.titles(); !slices.Equal(got, []string{"S"}) {
		t.Errorf("playlist = %v, the names must find it when the ISRC does not", got)
	}
	if searches, _, _, _ := f2.state(); searches != 2 {
		t.Errorf("%d searches, want the ISRC and then the names", searches)
	}
}

func TestRemixesOfTheSongComeAlong(t *testing.T) {
	f := &fakeSpotify{catalog: []fakeTrack{
		{URI: "spotify:track:D", Name: "Drugs", Artists: []string{"Ran-D"}, Date: "2026-10-29", Key: "drugs"},
		{URI: "spotify:track:DR", Name: "Drugs - Crypsis Remix", Artists: []string{"Ran-D", "Crypsis"}, Date: "2026-10-29", Key: "drugs"},
		{URI: "spotify:track:DO", Name: "Drugs (Old Remix)", Artists: []string{"Ran-D"}, Date: "2019-01-01", Key: "drugs"},    // too old
		{URI: "spotify:track:DX", Name: "Drugs - Remix", Artists: []string{"Someone Else"}, Date: "2026-10-29", Key: "drugs"}, // other artist
	}}
	sp := newFakeClient(t, f)
	c, _ := newCache(t)
	mustSync(t, sp, []hardstyle.Track{{ID: "a", Title: "Drugs", Artist: "Ran-D"}}, opts(nil), c)
	if got := f.titles(); !slices.Equal(got, []string{"D", "DR"}) {
		t.Errorf("playlist = %v, want the song and its remix, not the old remix or the one of another artist", got)
	}
	if e, _ := c.Entry(hardstyle.Track{ID: "a"}); e.URI != "spotify:track:D" || !slices.Equal(e.Extra, []string{"spotify:track:DR"}) {
		t.Errorf("entry = %+v", e)
	}
}

// The two sites often name different days for a release. A track with the fitting title and artist is taken even
// if the date is a few weeks off; the release is marked, so the screen says so.

// The two sites often name different days for a release. A track with the fitting title and artist is taken even
// if the date is a few weeks off; the release is marked, so the screen says so.
func TestADateAFewWeeksOffIsStillTheSameRelease(t *testing.T) {
	f := &fakeSpotify{catalog: []fakeTrack{
		{URI: "spotify:track:DM", Name: "Dance With Me", Artists: []string{"Sharkboy"}, Date: "2026-09-25", Key: "dance with me"},
		{URI: "spotify:track:DMX", Name: "Dance With Me - Remix", Artists: []string{"Sharkboy"}, Date: "2026-09-25", Key: "dance with me"},
	}}
	sp := newFakeClient(t, f)
	c, _ := newCache(t)
	rel := []hardstyle.Track{{ID: "a", Title: "Dance With Me", Artist: "Sharkboy"}}
	res := mustSync(t, sp, rel, opts(map[string]string{"Dance With Me": "2026-10-23"}), c) // 28 days apart
	if got := f.titles(); !slices.Equal(got, []string{"DM", "DMX"}) || len(res.Unmatched) != 0 {
		t.Errorf("playlist = %v unmatched = %v, want the song and its remix", got, res.Unmatched)
	}
	e, _ := c.Entry(rel[0])
	if e.URI != "spotify:track:DM" || e.DaysOff != 28 || e.Reason != "" {
		t.Errorf("entry = %+v, want the track with 28 days off", e)
	}
	if note := resolvedFrom(e, true).note(); note != i18n.T("Datum weicht um 28 Tage ab, trotzdem aufgenommen", "Date is 28 days off, taken anyway") {
		t.Errorf("note = %q", note)
	}
}

// A date months or years away is another release, even when the title is the same.

// A date months or years away is another release, even when the title is the same.
func TestTooMuchOfADateIsAnotherRelease(t *testing.T) {
	f := &fakeSpotify{catalog: []fakeTrack{
		{URI: "spotify:track:OLD", Name: "Dance With Me", Artists: []string{"Sharkboy"}, Date: "2026-01-05", Key: "dance with me"},         // 291 days
		{URI: "spotify:track:RMX", Name: "Dance With Me - Remix", Artists: []string{"Sharkboy"}, Date: "2019-09-25", Key: "dance with me"}, // years
	}}
	sp := newFakeClient(t, f)
	c, _ := newCache(t)
	rel := []hardstyle.Track{{ID: "a", Title: "Dance With Me", Artist: "Sharkboy"}}
	res := mustSync(t, sp, rel, opts(map[string]string{"Dance With Me": "2026-10-23"}), c)
	if got := f.titles(); len(got) != 0 || len(res.Unmatched) != 1 {
		t.Errorf("playlist = %v unmatched = %v, want nothing", got, res.Unmatched)
	}
	e, _ := c.Entry(rel[0])
	if e.Reason != spotify.ReasonDate || !strings.Contains(resolvedFrom(e, true).note(), "291") {
		t.Errorf("entry = %+v note = %q", e, resolvedFrom(e, true).note())
	}
}

// An album that is released again: its tracks stand right after each other on the site, and some are older.

func TestQuickRunDoesNothingWhenThereIsNothingNew(t *testing.T) {
	site := &fakeSite{tracks: siteTracks(6)}
	srv := site.start(t)
	f := &fakeSpotify{}
	sp := newFakeClient(t, f)
	sp.SetDelay(0)
	c, path := newCache(t)
	cfg := config.Config{PlaylistID: "p", Genres: []string{hardstyle.AnyGenre}, Pages: 5, MaxTracks: 500, MaxSearches: 100, DateTolerance: 14, CacheFile: path, Description: descriptionOff}

	// The first run is a full one: it reads everything and fills the playlist.
	res, err := Run(context.Background(), srv.Client(), sp, cfg, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 6 || !slices.Equal(f.titles(), []string{"T1", "T2", "T3", "T4", "T5", "T6"}) {
		t.Fatalf("first run: added=%d playlist=%v", res.Added, f.titles())
	}
	_ = c

	// A check: one page of the list, no request to Spotify at all.
	searches, reads, snaps, _ := f.state()
	site.hits = nil
	var shown []event.Event
	res, err = Run(context.Background(), srv.Client(), sp, cfg, false, true, func(ev event.Event) { shown = append(shown, ev) })
	if err != nil || res.Added != 0 || res.Scraped != 3 {
		t.Fatalf("check: res=%+v err=%v, want 3 looked at and nothing added", res, err)
	}
	if s2, r2, n2, _ := f.state(); s2 != searches || r2 != reads || n2 != snaps {
		t.Errorf("a check without news must not ask Spotify: searches %d->%d, reads %d->%d, snapshots %d->%d", searches, s2, reads, r2, snaps, n2)
	}
	if site.count("list") != 1 || site.count("detail") != 0 {
		t.Errorf("a check reads one list page and no detail page: %d list, %d detail", site.count("list"), site.count("detail"))
	}
	var items int
	for _, ev := range shown {
		if ev.Kind == event.EvItem {
			items++
		}
	}
	if items != 3 {
		t.Errorf("the screen must show the %d releases from the cache, got %d items", 3, items)
	}

	// Two new tracks appear on top: the next check finds them and adds them.
	site.mu.Lock()
	site.tracks = append([]hardstyle.Track{{ID: "n2", Title: "N2", Artist: "A"}, {ID: "n1", Title: "N1", Artist: "A"}}, site.tracks...)
	site.mu.Unlock()
	res, err = Run(context.Background(), srv.Client(), sp, cfg, false, true, nil)
	if err != nil || res.Added != 2 {
		t.Fatalf("check with news: res=%+v err=%v", res, err)
	}
	if got := f.titles(); !slices.Equal(got[:4], []string{"N2", "N1", "T1", "T2"}) || len(got) != 8 {
		t.Errorf("playlist = %v, the new tracks go on top and the rest stays", got)
	}
	if searches2, _, _, _ := f.state(); searches2-searches > 4 {
		t.Errorf("only the two new tracks are looked up, got %d searches", searches2-searches)
	}
}

func TestQuickRunCatchesAWholeAlbumOnSeveralPages(t *testing.T) {
	site := &fakeSite{tracks: siteTracks(3)}
	srv := site.start(t)
	f := &fakeSpotify{}
	sp := newFakeClient(t, f)
	_, path := newCache(t)
	cfg := config.Config{PlaylistID: "p", Genres: []string{hardstyle.AnyGenre}, Pages: 5, MaxTracks: 500, MaxSearches: 100, DateTolerance: 7, CacheFile: path, Description: descriptionOff}
	if _, err := Run(context.Background(), srv.Client(), sp, cfg, false, false, nil); err != nil {
		t.Fatal(err)
	}
	// 7 new tracks at once: more than two pages. The check reads on until it meets a known track.
	var news []hardstyle.Track
	for i := 7; i >= 1; i-- {
		news = append(news, hardstyle.Track{ID: fmt.Sprintf("n%d", i), Title: fmt.Sprintf("N%d", i), Artist: "A"})
	}
	site.mu.Lock()
	site.tracks = append(news, site.tracks...)
	site.mu.Unlock()
	res, err := Run(context.Background(), srv.Client(), sp, cfg, false, true, nil)
	if err != nil || res.Added != 7 {
		t.Fatalf("res=%+v err=%v, want all 7 new tracks", res, err)
	}
}
