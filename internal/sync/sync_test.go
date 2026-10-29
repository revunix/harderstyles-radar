package sync

import (
	"context"
	"errors"
	"fmt"
	"hardstylesync/internal/cache"
	"hardstylesync/internal/config"
	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/spotify"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPlaylistGetsTheOrderOfTheSite(t *testing.T) {
	f := &fakeSpotify{}
	f.setPlaylist("A", "B")
	sp := newFakeClient(t, f)
	c, _ := newCache(t)
	dates := map[string]string{"N2": "2026-10-29", "N1": "2026-10-29", "A": "2026-10-28", "B": "2026-10-27"}

	res := mustSync(t, sp, site("N2", "N1", "A", "B"), opts(dates), c)

	if got, want := f.titles(), []string{"N2", "N1", "A", "B"}; !slices.Equal(got, want) {
		t.Fatalf("playlist = %v, want %v", got, want)
	}
	if _, _, _, ops := f.state(); len(ops) != 1 || ops[0] != "POST@0:"+uri("N2")+","+uri("N1") {
		t.Errorf("a day with two new tracks needs one insert at the top, got %v", ops)
	}
	if res.Added != 2 || res.Removed != 0 || res.Reordered {
		t.Errorf("added=%d removed=%d reordered=%v", res.Added, res.Removed, res.Reordered)
	}
}

func TestWrongOrderIsFixedWithOneRewrite(t *testing.T) {
	f := &fakeSpotify{}
	f.setPlaylist("B", "A") // the site has A (newer) before B
	sp := newFakeClient(t, f)
	c, _ := newCache(t)
	res := mustSync(t, sp, site("A", "B"), opts(map[string]string{"A": "2026-10-28", "B": "2026-10-27"}), c)

	if got := f.titles(); !slices.Equal(got, []string{"A", "B"}) {
		t.Fatalf("playlist = %v", got)
	}
	if _, _, _, ops := f.state(); len(ops) != 1 || !strings.HasPrefix(ops[0], "PUT:") || !res.Reordered {
		t.Errorf("ops=%v reordered=%v, want one PUT", ops, res.Reordered)
	}
}

func TestNewestDateFirstAcrossGenres(t *testing.T) {
	f := &fakeSpotify{}
	sp := newFakeClient(t, f)
	c, _ := newCache(t)
	// The list of the site interleaves the genres, but the dates decide the order.
	mustSync(t, sp, site("OLD", "MID", "NEW"), opts(map[string]string{"OLD": "2026-10-24", "MID": "2026-10-27", "NEW": "2026-10-29"}), c)
	if got := f.titles(); !slices.Equal(got, []string{"NEW", "MID", "OLD"}) {
		t.Errorf("playlist = %v, want newest release date first", got)
	}
}

func TestOldestAtTheEndDropOut(t *testing.T) {
	f := &fakeSpotify{}
	f.setPlaylist("X1", "X2", "X3", "X4", "X5")
	sp := newFakeClient(t, f)
	c, _ := newCache(t)
	o := opts(nil)
	o.MaxTracks = 5
	res := mustSync(t, sp, site("N1", "N2"), o, c)

	if got := f.titles(); !slices.Equal(got, []string{"N1", "N2", "X1", "X2", "X3"}) {
		t.Errorf("playlist = %v", got)
	}
	if res.Added != 2 || res.Removed != 2 || res.After != 5 {
		t.Errorf("added=%d removed=%d after=%d, want 2/2/5", res.Added, res.Removed, res.After)
	}
}

func TestPlaylistIsReadOnlyWhenItChanged(t *testing.T) {
	f := &fakeSpotify{}
	f.setPlaylist("A")
	sp := newFakeClient(t, f)
	c, path := newCache(t)
	rel := site("A")

	mustSync(t, sp, rel, opts(nil), c)
	_, reads, snaps, _ := f.state()
	if reads != 1 || snaps != 1 {
		t.Fatalf("first run: %d item reads, %d snapshot reads, want 1/1", reads, snaps)
	}

	// A later run, a new process: nothing changed, so one request for the snapshot is all it takes.
	c, _ = cache.Load(path)
	mustSync(t, sp, rel, opts(nil), c)
	searches, reads, snaps, ops := f.state()
	if reads != 1 || snaps != 2 || len(ops) != 0 {
		t.Errorf("unchanged playlist: item reads=%d snapshot reads=%d ops=%v, want 1/2/none", reads, snaps, ops)
	}
	if searches != 1 {
		t.Errorf("the track is known, it must not be searched again (%d searches)", searches)
	}

	// Somebody changes the playlist by hand: the snapshot differs, the tool reads it again.
	f.setPlaylist("A", "ZZ")
	c, _ = cache.Load(path)
	mustSync(t, sp, rel, opts(nil), c)
	if _, reads, _, _ = f.state(); reads != 2 {
		t.Errorf("changed playlist: item reads=%d, want 2", reads)
	}
	if got := f.titles(); !slices.Equal(got, []string{"A", "ZZ"}) {
		t.Errorf("tracks of others stay: %v", got)
	}
}

func TestDateMustMatchTheSite(t *testing.T) {
	f := &fakeSpotify{dates: map[string]string{"T3": "2020-01-01"}} // an older song with the same name
	sp := newFakeClient(t, f)
	c, _ := newCache(t)

	res := mustSync(t, sp, site("T1", "T3"), opts(nil), c)
	if got := f.titles(); !slices.Equal(got, []string{"T1"}) {
		t.Errorf("playlist = %v, the track with the wrong date must stay out", got)
	}
	if len(res.Unmatched) != 1 {
		t.Fatalf("unmatched = %v", res.Unmatched)
	}
	e, _ := c.Entry(site("T3")[0])
	if e.Reason != spotify.ReasonDate || e.SpotifyDate != "2020-01-01" || e.Tries != 1 {
		t.Errorf("entry = %+v", e)
	}

	// Within the same day it is not asked again, after 12 hours it is.
	before, _, _, _ := f.state()
	mustSync(t, sp, site("T1", "T3"), opts(nil), c)
	if after, _, _, _ := f.state(); after != before {
		t.Errorf("searched again within the hour: %d -> %d", before, after)
	}
	c.MarkDue(site("T3")[0])
	mustSync(t, sp, site("T1", "T3"), opts(nil), c)
	if after, _, _, _ := f.state(); after <= before {
		t.Error("a missing track must be searched again at the next run")
	}
	if e, _ = c.Entry(site("T3")[0]); e.Tries != 2 {
		t.Errorf("tries = %d, want 2", e.Tries)
	}
}

func TestMissingTrackIsFoundWhenSpotifyGetsIt(t *testing.T) {
	f := &fakeSpotify{missing: map[string]bool{"T2": true}}
	sp := newFakeClient(t, f)
	c, _ := newCache(t)
	rel := site("T1", "T2")

	res := mustSync(t, sp, rel, opts(nil), c)
	if len(res.Unmatched) != 1 || !slices.Equal(f.titles(), []string{"T1"}) {
		t.Fatalf("unmatched=%v playlist=%v", res.Unmatched, f.titles())
	}

	f.mu.Lock()
	delete(f.missing, "T2") // Spotify has it now
	f.mu.Unlock()
	c.MarkDue(site("T2")[0])

	res = mustSync(t, sp, rel, opts(nil), c)
	if len(res.Unmatched) != 0 || !slices.Equal(f.titles(), []string{"T1", "T2"}) {
		t.Errorf("unmatched=%v playlist=%v, want T2 added", res.Unmatched, f.titles())
	}
}

func TestSearchLimitSpreadsTheWorkOverRuns(t *testing.T) {
	f := &fakeSpotify{}
	sp := newFakeClient(t, f)
	c, _ := newCache(t)
	rel := site("T1", "T2", "T3", "T4", "T5")
	o := opts(nil)
	o.MaxSearches = 2

	res := mustSync(t, sp, rel, o, c)
	if searches, _, _, _ := f.state(); searches != 2 || res.Deferred != 3 || res.Matched != 2 {
		t.Errorf("run 1: searches=%d deferred=%d matched=%d, want 2/3/2", searches, res.Deferred, res.Matched)
	}
	res = mustSync(t, sp, rel, o, c)
	if searches, _, _, _ := f.state(); searches != 4 || res.Deferred != 1 {
		t.Errorf("run 2: searches=%d deferred=%d, want 4/1", searches, res.Deferred)
	}
	mustSync(t, sp, rel, o, c)
	if got := f.titles(); len(got) != 5 {
		t.Errorf("after three runs the playlist has %v", got)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	f := &fakeSpotify{}
	sp := newFakeClient(t, f)
	c, _ := newCache(t)
	o := opts(nil)
	o.DryRun = true
	res := mustSync(t, sp, site("T1"), o, c)
	if _, _, _, ops := f.state(); res.Added != 1 || len(ops) != 0 || len(f.titles()) != 0 {
		t.Errorf("res=%+v ops=%v playlist=%v, want 1 planned and nothing written", res, ops, f.titles())
	}
}

func TestMakePlan(t *testing.T) {
	cases := []struct {
		name       string
		cur, want  []string
		kind       planKind
		head, tail []string
	}{
		{"same", []string{"a", "b"}, []string{"a", "b"}, planNone, nil, nil},
		{"new on top", []string{"a", "b"}, []string{"n", "a", "b"}, planIncremental, []string{"n"}, nil},
		{"new on top, old out", []string{"a", "b", "c"}, []string{"n1", "n2", "a", "b"}, planIncremental, []string{"n1", "n2"}, []string{"c"}},
		{"only old out", []string{"a", "b", "c"}, []string{"a", "b"}, planIncremental, nil, []string{"c"}},
		{"fill an empty playlist", nil, []string{"a", "b"}, planIncremental, []string{"a", "b"}, nil},
		{"wrong order", []string{"b", "a"}, []string{"a", "b"}, planRewrite, nil, nil},
		{"known track moved up", []string{"a", "b", "c"}, []string{"c", "a", "b"}, planRewrite, nil, nil},
		{"all new", []string{"a"}, []string{"x", "y"}, planIncremental, []string{"x", "y"}, []string{"a"}},
	}
	for _, c := range cases {
		p := makePlan(c.cur, c.want)
		if p.kind != c.kind || !slices.Equal(p.head, c.head) || !slices.Equal(p.tail, c.tail) {
			t.Errorf("%s: kind=%v head=%v tail=%v, want kind=%v head=%v tail=%v", c.name, p.kind, p.head, p.tail, c.kind, c.head, c.tail)
		}
	}
}

func TestDesiredOrder(t *testing.T) {
	known := []resolved{
		{uris: []string{"u1"}, date: "2026-10-27"},
		{uris: []string{"u2", "u2b"}, date: "2026-10-29"}, // two versions of the song
		{uris: []string{"u3"}, date: "2026-10-29"},
		{},                     // missing
		{uris: []string{"u4"}}, // date unknown: last of the found
		{uris: []string{"u2"}, date: "2026-10-29"}, // twice the same track
	}
	got := desiredOrder(known, []string{"old1", "u1", "old2"}, 100, false)
	want := []string{"u2", "u2b", "u3", "u1", "u4", "old1", "old2"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := desiredOrder(known, []string{"old1"}, 3, false); !slices.Equal(got, []string{"u2", "u2b", "u3"}) {
		t.Errorf("capped: %v", got)
	}
}

func TestOneListKeepsTheOrderOfTheSite(t *testing.T) {
	// The dates are not in order on purpose: with one list the order of the site wins.
	known := []resolved{
		{uris: []string{"u1"}, date: "2026-10-20"},
		{uris: []string{"u2"}, date: "2026-10-29"},
		{uris: []string{"u3"}, date: "2026-10-25"},
	}
	if got := desiredOrder(known, []string{"old"}, 100, true); !slices.Equal(got, []string{"u1", "u2", "u3", "old"}) {
		t.Errorf("one list: %v", got)
	}
	if got := desiredOrder(known, []string{"old"}, 100, false); !slices.Equal(got, []string{"u2", "u3", "u1", "old"}) {
		t.Errorf("several lists: %v", got)
	}

	f := &fakeSpotify{}
	sp := newFakeClient(t, f)
	c, _ := newCache(t)
	o := opts(map[string]string{"T1": "2026-10-27", "T2": "2026-10-29", "T3": "2026-10-28"})
	o.KeepSiteOrder = true
	mustSync(t, sp, site("T1", "T2", "T3"), o, c)
	if got := f.titles(); !slices.Equal(got, []string{"T1", "T2", "T3"}) {
		t.Errorf("playlist = %v, want the order of the list", got)
	}
}

func TestOneFailedLookupDoesNotEndTheRun(t *testing.T) {
	f := &fakeSpotify{failTitles: map[string]bool{"T2": true}}
	sp := newFakeClient(t, f)
	c, _ := newCache(t)

	res, err := Sync(context.Background(), sp, "p", site("T1", "T2", "T3"), opts(nil), c, nil)
	if err != nil {
		t.Fatalf("a failed lookup must not end the run: %v", err)
	}
	if got := f.titles(); !slices.Equal(got, []string{"T1", "T3"}) {
		t.Errorf("playlist = %v, the others must still be written", got)
	}
	if res.Deferred != 1 || res.Matched != 2 {
		t.Errorf("deferred=%d matched=%d, want 1/2", res.Deferred, res.Matched)
	}
	if _, ok := c.Entry(site("T2")[0]); ok {
		if e, _ := c.Entry(site("T2")[0]); !e.At.IsZero() {
			t.Error("a failed lookup is no answer, the cache must not remember it as missing")
		}
	}

	// At the next run it is tried again, and the others cost nothing.
	f.mu.Lock()
	delete(f.failTitles, "T2")
	f.mu.Unlock()
	before, _, _, _ := f.state()
	res = mustSync(t, sp, site("T1", "T2", "T3"), opts(nil), c)
	if after, _, _, _ := f.state(); after-before != 1 || !slices.Equal(f.titles(), []string{"T1", "T2", "T3"}) || res.Deferred != 0 {
		t.Errorf("second run: %d searches, playlist %v, deferred %d", after-before, f.titles(), res.Deferred)
	}
}

func TestManyFailuresInARowStopTheSearchButNotTheWriting(t *testing.T) {
	f := &fakeSpotify{failTitles: map[string]bool{}}
	for i := 2; i <= 9; i++ {
		f.failTitles[fmt.Sprintf("T%d", i)] = true
	}
	sp := newFakeClient(t, f)
	c, _ := newCache(t)

	res, err := Sync(context.Background(), sp, "p", site("T1", "T2", "T3", "T4", "T5", "T6", "T7", "T8", "T9"), opts(nil), c, nil)
	if err == nil || !strings.Contains(err.Error(), "hintereinander") {
		t.Fatalf("err = %v, want the stop to be reported", err)
	}
	if got := f.titles(); !slices.Equal(got, []string{"T1"}) {
		t.Errorf("playlist = %v, what was found must be written", got)
	}
	if res.Deferred != 8 {
		t.Errorf("deferred = %d, want the 8 that were not found out", res.Deferred)
	}
	if searches, _, _, _ := f.state(); searches > 1+maxLookupFailures*3 {
		t.Errorf("%d requests: the run must stop after %d failures", searches, maxLookupFailures)
	}
}

func TestRateLimitInTheMiddleStillWritesWhatWasFound(t *testing.T) {
	f := &fakeSpotify{limitAfter: 2}
	sp := newFakeClient(t, f)
	c, _ := newCache(t)

	res, err := Sync(context.Background(), sp, "p", site("T1", "T2", "T3", "T4"), opts(nil), c, nil)
	var limit *spotify.RateLimitError
	if !errors.As(err, &limit) {
		t.Fatalf("err = %v, want the rate limit reported", err)
	}
	if got := f.titles(); !slices.Equal(got, []string{"T1", "T2"}) {
		t.Errorf("playlist = %v, T1 and T2 were found before the limit and must be written", got)
	}
	if res.Added != 2 || res.Deferred != 2 {
		t.Errorf("added=%d deferred=%d, want 2/2", res.Added, res.Deferred)
	}
	if searches, _, _, _ := f.state(); searches != 3 {
		t.Errorf("%d searches, the run must stop at the first 429", searches)
	}
}

func TestRateLimitIsRememberedAndNotAskedAgain(t *testing.T) {
	f := &fakeSpotify{limited: true}
	sp := newFakeClient(t, f)
	c, path := newCache(t)
	sp.OnBlock = func(id string, until time.Time) { c.Block(id, until); c.Save() }

	start := time.Now()
	_, err := Sync(context.Background(), sp, "p", site("T1", "T2"), opts(nil), c, nil)
	var limit *spotify.RateLimitError
	if !errors.As(err, &limit) || limit.RetryAfter < 23*time.Hour || time.Since(start) > 5*time.Second {
		t.Fatalf("err = %v after %s: must report the long wait at once", err, time.Since(start))
	}
	if !strings.Contains(err.Error(), "Std.") {
		t.Errorf("message should say how long: %v", err)
	}
	if searches, _, _, _ := f.state(); searches != 1 {
		t.Errorf("%d searches, the run must stop at the first 429", searches)
	}

	// A new process knows about the block from the file and asks nothing.
	c2, _ := cache.Load(path)
	if until := c2.BlockedUntil("id"); until.IsZero() || time.Until(until) < 23*time.Hour {
		t.Fatalf("block not remembered: %v", until)
	}
	_, err = Run(context.Background(), http.DefaultClient, sp, config.Config{Credentials: []spotify.Credential{{ClientID: "id"}}, CacheFile: path, Genres: []string{"Hardstyle"}, Pages: 1, MaxTracks: 500}, false, false, nil)
	if !errors.As(err, &limit) {
		t.Errorf("Run during a block: err = %v, want a spotify.RateLimitError", err)
	}
	if searches, _, snaps, _ := f.state(); searches != 1 || snaps != 1 {
		t.Errorf("Run asked Spotify during the block: searches=%d snapshot reads=%d", searches, snaps)
	}
}

func TestInterleave(t *testing.T) {
	mk := func(names ...string) (l []hardstyle.Track) {
		for _, n := range names {
			l = append(l, hardstyle.Track{Title: n})
		}
		return
	}
	got := interleave([][]hardstyle.Track{mk("a1", "a2", "a3"), mk("b1"), mk("c1", "c2")})
	var titles []string
	for _, g := range got {
		titles = append(titles, g.Title)
	}
	if want := "a1 b1 c1 a2 c2 a3"; strings.Join(titles, " ") != want {
		t.Errorf("got %v, want %s", titles, want)
	}
}

func albumRelease(id, title, artist string) hardstyle.Track {
	return hardstyle.Track{ID: id, Title: title, Artist: artist}
}

// withCatalog gives the releases their catalog numbers on hardstyle.com and switches the album check on.
func withCatalog(o Options, catalogs map[string]string) Options {
	o.AlbumMinTracks = 2
	o.FetchDetails = func(_ context.Context, t hardstyle.Track) (hardstyle.Details, error) {
		return hardstyle.Details{Date: "2026-10-29", Catalog: catalogs[t.ID]}, nil
	}
	return o
}

// albumTrack is a track of the album "alb" of the made-up Spotify.
func albumTrack(u, name, artist string, hidden bool) fakeTrack {
	return fakeTrack{URI: "spotify:track:" + u, Name: name, Artists: []string{artist}, Date: "2026-10-29", Key: strings.ToLower(name),
		AlbumID: "alb", AlbumName: "Into The Void", AlbumType: "album", AlbumTotal: 4, Hidden: hidden}
}

func single(u, name, artist, date string) fakeTrack {
	return fakeTrack{URI: "spotify:track:" + u, Name: name, Artists: []string{artist}, Date: date, Key: strings.ToLower(name)}
}

// An album with tracks of several artists: every track comes from the album, none from a single of the same day.
func TestAlbumTracksComeFromTheSameAlbum(t *testing.T) {
	catalog := []fakeTrack{
		// The singles come first in the answers of the search, with the same date: alone each search takes them.
		single("S0", "Song Zero", "Act of Rage", "2026-10-29"),
		single("S1", "Song One", "Ran-D", "2026-10-29"),
		single("S2", "Song Two", "Rejecta", "2026-10-29"),
		{URI: "spotify:track:R1", Name: "Song One - Some Remix", Artists: []string{"Ran-D"}, Date: "2026-10-29", Key: "song one"},
		single("B", "Bonus Song", "Act of Rage", "2026-10-29"), // not on the album
		single("X", "Other Song", "Someone", "2026-10-29"),
		albumTrack("L0", "Song Zero", "Act of Rage", false),
		albumTrack("L1", "Song One", "Ran-D", false),
		albumTrack("L2", "Song Two", "Rejecta", false),
		albumTrack("L3", "Song Three", "Act of Rage", true), // no search finds it, only the track list of the album
	}
	rel := []hardstyle.Track{
		albumRelease("x", "Other Song", "Someone"),
		albumRelease("a0", "Song Zero", "Act of Rage"),
		albumRelease("a1", "Song One", "Ran-D"),
		albumRelease("a2", "Song Two", "Rejecta"),
		albumRelease("a3", "Song Three", "Act of Rage"),
		albumRelease("a4", "Bonus Song", "Act of Rage"),
	}
	cats := map[string]string{"x": "OTHER1", "a0": "MINUS288", "a1": "MINUS288", "a2": "MINUS288", "a3": "MINUS288", "a4": "MINUS288"}

	f := &fakeSpotify{catalog: catalog}
	sp := newFakeClient(t, f)
	c, _ := newCache(t)
	o := withCatalog(opts(nil), cats)
	mustSync(t, sp, rel, o, c)

	got := f.titles()
	for _, want := range []string{"X", "L0", "L1", "L2", "L3", "R1", "B"} {
		if !slices.Contains(got, want) {
			t.Errorf("%s is missing: playlist = %v", want, got)
		}
	}
	for _, no := range []string{"S0", "S1", "S2"} {
		if slices.Contains(got, no) {
			t.Errorf("the single %s must give way to the album: %v", no, got)
		}
	}
	if e, _ := c.Entry(rel[1]); e.URI != "spotify:track:L0" || e.AlbumID != "alb" || !e.Album {
		t.Errorf("entry of an album track = %+v", e)
	}
	if e, _ := c.Entry(rel[5]); e.URI != "spotify:track:B" {
		t.Errorf("a track that is not on the album keeps its own match: %+v", e)
	}
	if f.albumReads != 1 {
		t.Errorf("the track list is read once, read %d times", f.albumReads)
	}

	// The next run knows it all: no search, no track list. Only the bonus track is not on the album, and its
	// check costs nothing.
	searches, _, _, _ := f.state()
	mustSync(t, sp, rel, o, c)
	if s2, _, _, _ := f.state(); s2 != searches || f.albumReads != 1 {
		t.Errorf("the second run asked again: searches %d -> %d, album reads %d", searches, s2, f.albumReads)
	}
}

// When Spotify does not give the track list of an album, the tracks the searches found on it are taken.
func TestAlbumWithoutItsTrackList(t *testing.T) {
	catalog := []fakeTrack{
		single("S0", "Song Zero", "Act of Rage", "2026-10-29"),
		single("S1", "Song One", "Act of Rage", "2026-10-29"),
		albumTrack("L0", "Song Zero", "Act of Rage", false),
		albumTrack("L1", "Song One", "Act of Rage", false),
	}
	rel := []hardstyle.Track{albumRelease("a0", "Song Zero", "Act of Rage"), albumRelease("a1", "Song One", "Act of Rage")}
	f := &fakeSpotify{catalog: catalog, failAlbums: true}
	sp := newFakeClient(t, f)
	c, _ := newCache(t)
	mustSync(t, sp, rel, withCatalog(opts(nil), map[string]string{"a0": "C1", "a1": "C1"}), c)
	if got := f.titles(); !slices.Equal(got, []string{"L0", "L1"}) {
		t.Errorf("playlist = %v, want the album tracks", got)
	}
}

// An album that is released again has old songs in it. The search may find only their old singles; the
// album's version is taken, not the old single.
func TestRereleasedAlbumTakesTheAlbumVersion(t *testing.T) {
	catalog := []fakeTrack{
		single("O0", "Album Track 0", "Dual Damage", "2023-11-16"),
		single("O1", "Album Track 1", "Dual Damage", "2025-06-26"),
		albumTrack("L0", "Album Track 0", "Dual Damage", true),
		albumTrack("L1", "Album Track 1", "Dual Damage", true),
		albumTrack("L2", "Album Track 2", "Dual Damage", false),
		albumTrack("L3", "Album Track 3", "Dual Damage", false),
		single("LO", "Lonely Old Song", "Third", "2017-01-01"),
	}
	rel := []hardstyle.Track{albumRelease("x", "Lonely Old Song", "Third")}
	cats := map[string]string{"x": "SOLO1"}
	for i := range 4 {
		id := fmt.Sprint("a", i)
		rel = append(rel, albumRelease(id, fmt.Sprint("Album Track ", i), "Dual Damage"))
		cats[id] = "DD2026"
	}

	f := &fakeSpotify{catalog: catalog}
	sp := newFakeClient(t, f)
	c, _ := newCache(t)
	o := withCatalog(opts(nil), cats)
	res := mustSync(t, sp, rel, o, c)
	if got := f.titles(); !slices.Equal(got, []string{"L0", "L1", "L2", "L3"}) {
		t.Errorf("playlist = %v, want the four tracks of the album", got)
	}
	if len(res.Unmatched) != 1 || res.Unmatched[0].ID != "x" {
		t.Errorf("unmatched = %v, want only the old single of another release", res.Unmatched)
	}

	// Switched off: the old ones are date mismatches again.
	f2 := &fakeSpotify{catalog: catalog}
	c2, _ := newCache(t)
	o.AlbumMinTracks = 0
	mustSync(t, newFakeClient(t, f2), rel, o, c2)
	if got := f2.titles(); !slices.Equal(got, []string{"L2", "L3"}) {
		t.Errorf("without the album check only the tracks the search finds go in: %v", got)
	}
}

// Tracks of an album that wait for the search limit come from the track list of the album, without a search.
func TestAlbumTracksNeedNoSearchOfTheirOwn(t *testing.T) {
	var catalog []fakeTrack
	var rel []hardstyle.Track
	cats := map[string]string{}
	for i := range 4 {
		name := fmt.Sprint("Album Track ", i)
		catalog = append(catalog, albumTrack(fmt.Sprint("L", i), name, "Dual Damage", false))
		id := fmt.Sprint("a", i)
		rel = append(rel, albumRelease(id, name, "Dual Damage"))
		cats[id] = "DD2026"
	}
	f := &fakeSpotify{catalog: catalog}
	sp := newFakeClient(t, f)
	c, _ := newCache(t)
	o := withCatalog(opts(nil), cats)
	o.MaxSearches = 2
	res := mustSync(t, sp, rel, o, c)
	if got := f.titles(); !slices.Equal(got, []string{"L0", "L1", "L2", "L3"}) {
		t.Errorf("playlist = %v", got)
	}
	if searches, _, _, _ := f.state(); searches != 2 || res.Deferred != 0 {
		t.Errorf("searches = %d, deferred = %d; want 2 and 0", searches, res.Deferred)
	}
}

// Without an album on Spotify (only singles), every track keeps what its search found.
func TestNoAlbumOnSpotifyKeepsTheSingles(t *testing.T) {
	catalog := []fakeTrack{
		single("S0", "Song Zero", "Act of Rage", "2026-10-29"),
		single("S1", "Song One", "Act of Rage", "2026-10-29"),
		// An old album of the artist with both songs on it is no album of this release.
		{URI: "spotify:track:OA0", Name: "Song Zero", Artists: []string{"Act of Rage"}, Date: "2019-05-01", Key: "song zero", AlbumID: "old", AlbumName: "Old", AlbumType: "album", AlbumTotal: 12},
		{URI: "spotify:track:OA1", Name: "Song One", Artists: []string{"Act of Rage"}, Date: "2019-05-01", Key: "song one", AlbumID: "old", AlbumName: "Old", AlbumType: "album", AlbumTotal: 12},
	}
	rel := []hardstyle.Track{albumRelease("a0", "Song Zero", "Act of Rage"), albumRelease("a1", "Song One", "Act of Rage")}
	f := &fakeSpotify{catalog: catalog}
	sp := newFakeClient(t, f)
	c, _ := newCache(t)
	mustSync(t, sp, rel, withCatalog(opts(nil), map[string]string{"a0": "C1", "a1": "C1"}), c)
	if got := f.titles(); !slices.Equal(got, []string{"S0", "S1"}) || f.albumReads != 0 {
		t.Errorf("playlist = %v, album reads = %d; want the singles and no read", got, f.albumReads)
	}
}

// Tracks of an album found before the album check are looked up once more, to know their Spotify album.
func TestOldAlbumEntriesAreSearchedOnce(t *testing.T) {
	catalog := []fakeTrack{
		single("S0", "Song Zero", "Act of Rage", "2026-10-29"),
		single("S1", "Song One", "Act of Rage", "2026-10-29"),
		albumTrack("L0", "Song Zero", "Act of Rage", false),
		albumTrack("L1", "Song One", "Act of Rage", false),
	}
	rel := []hardstyle.Track{albumRelease("a0", "Song Zero", "Act of Rage"), albumRelease("a1", "Song One", "Act of Rage")}
	c, _ := newCache(t)
	for i, r := range rel {
		c.SetDetails(r, hardstyle.Details{Date: "2026-10-29", Catalog: "C1"})
		c.SetFound(r, cache.Found{URIs: []string{fmt.Sprint("spotify:track:S", i)}})
	}
	f := &fakeSpotify{catalog: catalog}
	sp := newFakeClient(t, f)
	o := withCatalog(opts(nil), map[string]string{"a0": "C1", "a1": "C1"})
	mustSync(t, sp, rel, o, c)
	if got := f.titles(); !slices.Equal(got, []string{"L0", "L1"}) {
		t.Errorf("playlist = %v", got)
	}
	first, _, _, _ := f.state()
	mustSync(t, sp, rel, o, c)
	if again, _, _, _ := f.state(); first != 2 || again != first {
		t.Errorf("searches = %d, then %d; want 2 and no more", first, again)
	}
}

func TestGroupsOf(t *testing.T) {
	cats := []string{"A", "B", "A", "", "C", "A", "B"}
	rel := make([]hardstyle.Track, len(cats))
	of := func(i int) string { return cats[i] }
	got := groupsOf(rel, of, 2)
	if len(got) != 2 || got[0].catalog != "A" || !slices.Equal(got[0].members, []int{0, 2, 5}) || got[1].catalog != "B" {
		t.Errorf("groups = %+v", got)
	}
	if got := groupsOf(rel, of, 3); len(got) != 1 {
		t.Errorf("with 3 only A is an album: %+v", got)
	}
	if groupsOf(rel, of, 0) != nil {
		t.Error("0 switches it off")
	}
}

// ---- the page of hardstyle.com and the service ----

// fakeSite is a small hardstyle.com: a list of tracks with 3 per page, and a detail page for each.
