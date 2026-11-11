package sync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"hardstylesync/internal/config"
	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/hardtunes"
	"hardstylesync/internal/spotify"
)

func TestAReleaseThatIsNotOutYetIsNotSearched(t *testing.T) {
	f := &fakeSpotify{}
	sp := newFakeClient(t, f)
	c, _ := newCache(t)
	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	res := mustSync(t, sp, site("T1", "T2"), opts(map[string]string{"T1": tomorrow, "T2": "2026-10-29"}), c)
	if searches, _, _, _ := f.state(); searches != 1 || res.Deferred != 1 || !slices.Equal(f.titles(), []string{"T2"}) {
		t.Errorf("searches=%d deferred=%d playlist=%v: only the release that is out is searched", searches, res.Deferred, f.titles())
	}
}

func TestTwoRecordingsThatFitEquallyAreLeftToACorrection(t *testing.T) {
	f := &fakeSpotify{catalog: []fakeTrack{
		{URI: "spotify:track:EXT", Name: "Song - Extended Mix", Artists: []string{"A"}, Date: "2026-10-29", Key: "song", Seconds: 330},
		{URI: "spotify:track:RAD", Name: "Song - Radio Edit", Artists: []string{"A"}, Date: "2026-10-29", Key: "song", Seconds: 190},
	}}
	sp := newFakeClient(t, f)
	c, _ := newCache(t)
	// The site names neither the version nor the length: nothing tells the two apart.
	res := mustSync(t, sp, []hardstyle.Track{{ID: "1", Title: "Song", Artist: "A"}}, opts(nil), c)
	if len(res.Uncertain) != 1 || len(f.titles()) != 0 || len(res.Unmatched) != 0 {
		t.Errorf("uncertain=%v unmatched=%v playlist=%v: no guess, the release waits for a correction", res.Uncertain, res.Unmatched, f.titles())
	}
	// With the length of the site the extended mix is clear.
	c2, _ := newCache(t)
	f2 := &fakeSpotify{catalog: f.catalog}
	sp2 := newFakeClient(t, f2)
	mustSync(t, sp2, []hardstyle.Track{{ID: "1", Title: "Song", Artist: "A", Duration: 331}}, opts(nil), c2)
	if got := f2.ops; len(got) == 0 || !strings.Contains(got[len(got)-1], "spotify:track:EXT") || strings.Contains(got[len(got)-1], "RAD") {
		t.Errorf("ops=%v: the length picks the extended mix", got)
	}
}

func TestAnISRCMatchTeachesTheArtistAndIsKeptForTheEvaluation(t *testing.T) {
	f := &fakeSpotify{catalog: []fakeTrack{
		{URI: "spotify:track:S", Name: "Hotrod", Artists: []string{"DJ Smurf", "Loftgroover"}, Date: "2026-10-29", Key: "hotrod", ISRC: "GB1234567890"},
	}}
	sp := newFakeClient(t, f)
	c, _ := newCache(t)
	o := opts(nil)
	o.EvalSamples = 1
	o.FetchDetails = func(context.Context, hardstyle.Track) (hardstyle.Details, error) {
		return hardstyle.Details{Date: "2026-10-29", ISRC: "GB1234567890"}, nil
	}
	tr := hardstyle.Track{ID: "1", Title: "Hotrod", Artist: "Smurf & Loftgroover"}
	mustSync(t, sp, []hardstyle.Track{tr}, o, c)

	e, _ := c.Entry(tr)
	if e.URI != "spotify:track:S" || !e.ByISRC || len(e.Searched) == 0 || e.Release == nil || e.Release.Title != "Hotrod" {
		t.Fatalf("entry = %+v: an ISRC match, with the title search kept for the evaluation", e)
	}
	// "Loftgroover" pairs by name; "Smurf" is left over with "DJ Smurf": the same artist under two names.
	if got := c.ArtistIDs("smurf"); !slices.Equal(got, []string{"id-DJ Smurf"}) {
		t.Errorf("smurf -> %v", got)
	}
	if got := c.ArtistIDs("loftgroover"); !slices.Equal(got, []string{"id-Loftgroover"}) {
		t.Errorf("loftgroover -> %v", got)
	}
}

func TestRunWarnsAndGoesOnWhenAListIsBroken(t *testing.T) {
	site := &fakeSite{tracks: siteTracks(3)}
	srv := site.start(t)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer broken.Close()
	before := hardtunes.Base
	hardtunes.Base = broken.URL
	t.Cleanup(func() { hardtunes.Base = before })

	f := &fakeSpotify{}
	sp := newFakeClient(t, f)
	sp.SetDelay(0)
	_, path := newCache(t)
	cfg := config.Config{PlaylistID: "p", Genres: []string{hardstyle.AnyGenre}, HardtunesGenres: []string{"terror"}, Pages: 5,
		MaxTracks: 10, MaxSearches: 100, DateTolerance: 14, CacheFile: path, Description: descriptionOff, Order: config.OrderNewest}
	res, err := Run(context.Background(), srv.Client(), sp, cfg, false, false, nil)
	if err != nil {
		t.Fatalf("a broken list must not end the run: %v", err)
	}
	if len(f.titles()) != 3 || !slices.ContainsFunc(res.Warnings, func(w string) bool { return strings.Contains(w, "hardtunes.com Terror") }) {
		t.Errorf("playlist=%v warnings=%v: the good list goes in, the broken one is reported", f.titles(), res.Warnings)
	}
}

func TestAnOldRecordingIsNotTakenForANewRelease(t *testing.T) {
	// Spotify has the recording only as a 2023 re-master; the release is from 2026.
	f := &fakeSpotify{catalog: []fakeTrack{
		{URI: "spotify:track:OLD", Name: "Song", Artists: []string{"A"}, Date: "2023-05-01", Key: "song", ISRC: "GB1"},
	}}
	sp := newFakeClient(t, f)
	c, _ := newCache(t)
	o := opts(nil)
	o.FetchDetails = func(context.Context, hardstyle.Track) (hardstyle.Details, error) {
		return hardstyle.Details{Date: "2026-10-29", ISRC: "GB1"}, nil
	}
	tr := hardstyle.Track{ID: "1", Title: "Song", Artist: "A"}
	res := mustSync(t, sp, []hardstyle.Track{tr}, o, c)
	if len(f.titles()) != 0 || len(res.Unmatched) != 1 {
		t.Fatalf("playlist=%v unmatched=%v: a track from 2023 must not enter a playlist of a 2026 release", f.titles(), res.Unmatched)
	}
	if e, _ := c.Entry(tr); e.URI != "" || e.Reason != spotify.ReasonDate {
		t.Errorf("entry = %+v, want missing with a date mismatch", e)
	}
}

func TestSinceIsTheOldestReleaseLessTheGrace(t *testing.T) {
	c, _ := newCache(t)
	a := hardstyle.Track{ID: "a", Path: "/en/tracks/a", Title: "A"}
	b := hardstyle.Track{ID: "b", Path: "/en/tracks/b", Title: "B"}
	c.SetDetails(a, hardstyle.Details{Date: "2026-10-29"})
	c.SetDetails(b, hardstyle.Details{Date: "2026-10-02"})
	if got := sinceOf([]hardstyle.Track{a, b}, (&window{}).listDate, c); got != "2026-08-18" {
		t.Errorf("since = %q, want the oldest day (2026-10-02) less %d days", got, 45)
	}
}
