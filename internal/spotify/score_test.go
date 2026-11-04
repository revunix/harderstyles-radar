package spotify

import (
	"context"
	"fmt"
	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/match"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

// track is a Spotify track of the tests, released on the day of the release unless the test changes it.
func track(uri, name string, length int, artists ...string) Track {
	return Track{URI: "spotify:track:" + uri, Name: name, Artists: artists, ReleaseDate: "2026-10-29", DatePrecision: "day",
		Duration: length, Album: Album{ID: "single-" + uri, Name: name, Type: "single", Tracks: 1}}
}

func uris(ids ...string) []string {
	var out []string
	for _, id := range ids {
		out = append(out, "spotify:track:"+id)
	}
	return out
}

func on(t Track, date string) Track {
	t.ReleaseDate = date
	return t
}

func TestDecide(t *testing.T) {
	day := hardstyle.Details{Date: "2026-10-29"}
	cases := []struct {
		name   string
		site   hardstyle.Track
		d      hardstyle.Details
		found  []Track
		o      MatchOptions
		want   []string
		reason string
	}{
		{
			name: "the length tells the Extended Mix from the Radio Edit",
			site: hardstyle.Track{Title: "Song", Artist: "A"}, d: hardstyle.Details{Date: "2026-10-29", Duration: 330},
			found: []Track{track("R", "Song - Radio Edit", 190, "A"), track("E", "Song - Extended Mix", 331, "A")},
			want:  uris("E"),
		},
		{
			name: "the length of the list, when the page names none",
			site: hardstyle.Track{Title: "Song", Artist: "A", Duration: 190}, d: day,
			found: []Track{track("E", "Song - Extended Mix", 331, "A"), track("R", "Song - Radio Edit", 191, "A")},
			want:  uris("R"),
		},
		{
			name: "the cut the site names, without lengths",
			site: hardstyle.Track{Title: "Song", Version: "Extended Mix", Artist: "A"}, d: day,
			found: []Track{track("R", "Song - Radio Edit", 0, "A"), track("E", "Song - Extended Mix", 0, "A")},
			want:  uris("E"),
		},
		{
			name: "a Radio Edit alone is not the Extended Mix of another length",
			site: hardstyle.Track{Title: "Song", Version: "Extended Mix", Artist: "A", Duration: 330}, d: day,
			found:  []Track{track("R", "Song - Radio Edit", 190, "A")},
			reason: ReasonNotFound,
		},
		{
			name: "two recordings that fit equally well: neither is taken",
			site: hardstyle.Track{Title: "Song", Artist: "A"}, d: day,
			found:  []Track{track("R", "Song - Radio Edit", 190, "A"), track("E", "Song - Extended Mix", 331, "A")},
			reason: ReasonUncertain,
		},
		{
			name: "the same recording on a single and on an album is no competitor",
			site: hardstyle.Track{Title: "Song", Artist: "A"}, d: day,
			found: []Track{track("S", "Song", 200, "A"), track("L", "Song", 201, "A")},
			want:  uris("S"),
		},
		{
			name: "remixes come along and are no competitors; one recording of each",
			site: hardstyle.Track{Title: "Drugs", Artist: "Ran-D"}, d: day,
			found: []Track{
				track("DR", "Drugs - Crypsis Remix", 240, "Ran-D", "Crypsis"),
				track("D", "Drugs", 230, "Ran-D"),
				track("DRA", "Drugs - Crypsis Remix", 240, "Ran-D", "Crypsis"), // the remix again, on an album
				track("V", "Drugs (VIP)", 250, "Ran-D"),
				track("X", "Drugs - Remix", 240, "Someone Else"),
			},
			want: uris("D", "V", "DR"), // the remixes by score: the VIP has the artist of the site alone
		},
		{
			name: "a remix alone is not the release",
			site: hardstyle.Track{Title: "Drugs", Artist: "Ran-D"}, d: day,
			found:  []Track{track("DR", "Drugs - Crypsis Remix", 0, "Ran-D", "Crypsis")},
			reason: ReasonNotFound,
		},
		{
			name: "an artist known by its Spotify id under another name",
			site: hardstyle.Track{Title: "Song", Artist: "Sub Zero Project"}, d: day,
			found: []Track{func() Track { c := track("S", "Song", 0, "SZP"); c.ArtistIDs = []string{"id1"}; return c }()},
			o: MatchOptions{ArtistIDs: func(name string) []string {
				if name == "sub zero project" {
					return []string{"id1"}
				}
				return nil
			}},
			want: uris("S"),
		},
		{
			name: "without the id the other name is another artist",
			site: hardstyle.Track{Title: "Song", Artist: "Sub Zero Project"}, d: day,
			found:  []Track{func() Track { c := track("S", "Song", 0, "SZP"); c.ArtistIDs = []string{"id1"}; return c }()},
			reason: ReasonNotFound,
		},
		{
			name: "a date beyond the grace is another release",
			site: hardstyle.Track{Title: "Song", Artist: "A"}, d: day, o: MatchOptions{Tolerance: 7},
			found: []Track{
				on(track("OLD", "Song", 0, "A"), "2026-01-05"),
				on(track("RMX", "Song - Remix", 0, "A"), "2019-09-25"),
			},
			reason: ReasonDate,
		},
		{
			name: "a date within the grace is still the release",
			site: hardstyle.Track{Title: "Song", Artist: "A"}, d: day, o: MatchOptions{Tolerance: 7},
			found: []Track{on(track("S", "Song", 0, "A"), "2026-10-01")},
			want:  uris("S"),
		},
		{
			name: "a higher MinScore takes nothing",
			site: hardstyle.Track{Title: "Song", Artist: "A"}, d: day, o: MatchOptions{MinScore: 10},
			found:  []Track{track("S", "Song", 0, "A")},
			reason: ReasonNotFound,
		},
		{
			name: "nothing found",
			site: hardstyle.Track{Title: "Song", Artist: "A"}, d: day,
			reason: ReasonNotFound,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Decide(c.site, c.d, c.found, c.o)
			if !slices.Equal(got.URIs, c.want) || got.Reason != c.reason {
				t.Errorf("URIs = %v reason = %q, want %v %q", got.URIs, got.Reason, c.want, c.reason)
			}
		})
	}
}

func TestDecideDetails(t *testing.T) {
	site := hardstyle.Track{Title: "Song", Artist: "A"}
	d := hardstyle.Details{Date: "2026-10-29"}

	// A date mismatch names the nearest date of a track whose title and artist fit; the candidates keep every
	// fitting track whatever its date, in the order of the search.
	found := []Track{on(track("A1", "Song", 0, "A"), "2019-01-01"), on(track("A2", "Song", 0, "A"), "2026-01-05"), track("X", "Other", 0, "B")}
	got := Decide(site, d, found, MatchOptions{Tolerance: 7})
	if got.Reason != ReasonDate || got.SpotifyDate != "2026-01-05" {
		t.Errorf("reason = %q date = %q, want the nearer date", got.Reason, got.SpotifyDate)
	}
	if len(got.Candidates) != 2 || got.Candidates[0].Track.URI != "spotify:track:A1" || got.Candidates[0].Kind != match.Exact {
		t.Errorf("candidates = %+v", got.Candidates)
	}
	if len(got.Searched) != 3 || got.Searched[2].URI != "spotify:track:X" {
		t.Errorf("searched = %v, want all three, the one that does not fit last", got.Searched)
	}

	// A date off within the grace is marked.
	got = Decide(site, d, []Track{on(track("S", "Song", 0, "A"), "2026-10-01")}, MatchOptions{Tolerance: 7})
	if got.DaysOff != 28 || got.SpotifyDate != "2026-10-01" {
		t.Errorf("days off = %d date = %q", got.DaysOff, got.SpotifyDate)
	}

	// Searched keeps the best maxSearched, by score.
	var many []Track
	for i := range 12 {
		many = append(many, track(fmt.Sprint("N", i), fmt.Sprint("Nope ", i), 0, "Z"))
	}
	many = append(many, track("S", "Song", 0, "A"))
	got = Decide(site, d, many, MatchOptions{})
	if len(got.Searched) != maxSearched || got.Searched[0].URI != "spotify:track:S" {
		t.Errorf("searched = %d tracks, first %q", len(got.Searched), got.Searched[0].URI)
	}
}

// On a tie the nearer date wins, then the order of the search answers, whatever order the tracks come in.
func TestDecideTieOrder(t *testing.T) {
	site := hardstyle.Track{Title: "Song", Artist: "A"}
	d := hardstyle.Details{Date: "2026-10-29"}
	near, far := track("NEAR", "Song", 0, "A"), on(track("FAR", "Song", 0, "A"), "2026-10-27")
	for _, found := range [][]Track{{near, far}, {far, near}} {
		if got := Decide(site, d, found, MatchOptions{Tolerance: 7}); !slices.Equal(got.URIs, uris("NEAR")) {
			t.Errorf("%v: URIs = %v, want the nearer date", found, got.URIs)
		}
	}
	a, b := track("A", "Song", 0, "A"), track("B", "Song", 0, "A")
	for range 3 {
		if got := Decide(site, d, []Track{a, b}, MatchOptions{}); !slices.Equal(got.URIs, uris("A")) {
			t.Errorf("URIs = %v, want the first of equal tracks", got.URIs)
		}
	}
	if got := Decide(site, d, []Track{b, a, b}, MatchOptions{}); !slices.Equal(got.URIs, uris("B")) {
		t.Errorf("URIs = %v, want the first of equal tracks", got.URIs)
	}
}

func TestScore(t *testing.T) {
	site := hardstyle.Track{Title: "Song", Version: "Extended Mix", Artist: "A and B", Duration: 330}
	d := hardstyle.Details{Date: "2026-10-29"}
	both := Score(site, d, track("1", "Song - Extended Mix", 330, "A", "B"), MatchOptions{})
	one := Score(site, d, track("2", "Song - Extended Mix", 330, "A"), MatchOptions{})
	radio := Score(site, d, track("3", "Song - Radio Edit", 190, "A", "B"), MatchOptions{})
	late := Score(site, d, on(track("4", "Song - Extended Mix", 330, "A", "B"), "2026-11-28"), MatchOptions{})
	if !(both.Score > one.Score && one.Score > radio.Score) || both.Kind != match.Exact {
		t.Errorf("scores: both artists %v, one %v, radio %v", both, one.Score, radio.Score)
	}
	if !(late.Score < both.Score) || late.Diff != 30 {
		t.Errorf("a later date costs: %v against %v, diff %d", late.Score, both.Score, late.Diff)
	}
}

func TestStoredRoundTrip(t *testing.T) {
	tr := Track{URI: "u", Name: "n", Artists: []string{"a", "b"}, ArtistIDs: []string{"1", ""}, ReleaseDate: "2026-10",
		DatePrecision: "month", Duration: 201, Album: Album{ID: "al", Name: "Al", Type: "album", Tracks: 12}}
	if got := TrackFromStored(tr.Stored()); !reflect.DeepEqual(got, tr) {
		t.Errorf("round trip = %+v, want %+v", got, tr)
	}
}

// fakeSearch is a Spotify that answers searches: by ISRC from isrc, else every track whose name starts with
// the first word of the query.
type fakeSearch struct {
	mu      sync.Mutex
	queries []string
	isrc    map[string][]string // ISRC -> items
	items   []string
}

func (f *fakeSearch) client(t *testing.T) *Client {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"access_token":"tok","expires_in":3600}`)
	})
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		f.mu.Lock()
		f.queries = append(f.queries, q)
		f.mu.Unlock()
		var items []string
		if code, ok := strings.CutPrefix(q, "isrc:"); ok {
			items = f.isrc[code]
		} else if q == "Song A" {
			items = f.items
		}
		fmt.Fprintf(w, `{"tracks":{"items":[%s]}}`, strings.Join(items, ","))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New(srv.Client(), "id", "secret", "refresh", WithEndpoints(srv.URL, srv.URL+"/token"), WithRetryWait(0))
	c.SetDelay(0)
	return c
}

func item(id, name string, ms int) string {
	return itemOn(id, name, ms, "2026-10-29")
}

// itemOn is an item of a search answer released on the given date ("2026-10-29", "2026-10" or "2026").
func itemOn(id, name string, ms int, date string) string {
	precision := "day"
	switch len(date) {
	case 4:
		precision = "year"
	case 7:
		precision = "month"
	}
	return fmt.Sprintf(`{"uri":"spotify:track:%s","name":%q,"duration_ms":%d,"artists":[{"id":"ar1","name":"A"}],`+
		`"album":{"id":"al-%s","name":%q,"album_type":"single","total_tracks":1,"release_date":%q,"release_date_precision":%q}}`,
		id, name, ms, id, name, date, precision)
}

func TestISRCDoesNotTakeATrackOfAnotherYear(t *testing.T) {
	ctx := context.Background()
	site := hardstyle.Track{Title: "Song", Artist: "A"}
	d := hardstyle.Details{Date: "2026-10-29", ISRC: "NL1"}

	// The recording lies on Spotify only as an old re-master; the title search finds the new release.
	f := &fakeSearch{isrc: map[string][]string{"NL1": {itemOn("OLD", "Song", 200000, "2023-05-01")}},
		items: []string{itemOn("NEW", "Song", 200000, "2026-10-29")}}
	look, err := f.client(t).FindTrack(ctx, site, d, MatchOptions{})
	if err != nil || !slices.Equal(look.URIs, uris("NEW")) || look.ByISRC {
		t.Errorf("look = %+v err = %v, want the release of this year, not the re-master", look, err)
	}

	// Only the old one exists: nothing is taken, and the answer names the date that was too far off.
	f = &fakeSearch{isrc: map[string][]string{"NL1": {itemOn("OLD", "Song", 200000, "2023-05-01")}}}
	look, err = f.client(t).FindTrack(ctx, site, d, MatchOptions{})
	if err != nil || len(look.URIs) != 0 || look.Reason != ReasonDate || look.SpotifyDate != "2023-05-01" {
		t.Errorf("look = %+v err = %v, want nothing taken and a date mismatch", look, err)
	}
}

func TestISRCTakesATrackWithinTheGrace(t *testing.T) {
	ctx := context.Background()
	f := &fakeSearch{isrc: map[string][]string{"NL1": {itemOn("I", "Song", 200000, "2026-10-05")}}}
	site := hardstyle.Track{Title: "Song", Artist: "A"}
	look, err := f.client(t).FindTrack(ctx, site, hardstyle.Details{Date: "2026-10-29", ISRC: "NL1"}, MatchOptions{Tolerance: 14})
	if err != nil || !slices.Equal(look.URIs, uris("I")) || !look.ByISRC || look.DaysOff != 24 {
		t.Errorf("look = %+v err = %v, want the same recording 24 days off, taken and marked", look, err)
	}
}

// yearOnly is a track whose Spotify date names only the year.
func yearOnly(t Track, y string) Track {
	t.ReleaseDate, t.DatePrecision = y, "year"
	return t
}

func TestWithoutASiteDateOnlyTheWindowFloorHolds(t *testing.T) {
	site := hardstyle.Track{Title: "Song", Artist: "A"}
	for _, c := range []struct {
		name string
		t    Track
		want []string
	}{
		{"old track", on(track("OLD", "Song", 200, "A"), "2023-05-01"), nil},
		{"new track", track("NEW", "Song", 200, "A"), uris("NEW")},
		{"old track, a year only", yearOnly(track("Y", "Song", 200, "A"), "2023"), nil},
		{"this year, a year only", yearOnly(track("Y", "Song", 200, "A"), "2026"), uris("Y")},
	} {
		look := Decide(site, hardstyle.Details{}, []Track{c.t}, MatchOptions{Since: "2026-09-01"})
		if !slices.Equal(look.URIs, c.want) {
			t.Errorf("%s: uris = %v, want %v", c.name, look.URIs, c.want)
		}
	}
}

func TestFindTrack(t *testing.T) {
	ctx := context.Background()
	site := hardstyle.Track{Title: "Song", Artist: "A"}

	// The search answer gives the length and the artist ids; the first query decides alone.
	f := &fakeSearch{items: []string{item("S", "Song", 200400)}}
	look, err := f.client(t).FindTrack(ctx, site, hardstyle.Details{Date: "2026-10-29"}, MatchOptions{})
	if err != nil || !slices.Equal(look.URIs, uris("S")) || look.ByISRC || len(f.queries) != 1 {
		t.Fatalf("look = %+v err = %v queries = %v", look, err, f.queries)
	}
	if got := look.Searched[0]; got.Duration != 200 || !slices.Equal(got.ArtistIDs, []string{"ar1"}) {
		t.Errorf("searched track = %+v", got)
	}

	// Nothing in the first query: the fielded query second.
	f = &fakeSearch{}
	look, err = f.client(t).FindTrack(ctx, site, hardstyle.Details{Date: "2026-10-29"}, MatchOptions{})
	if err != nil || look.Reason != ReasonNotFound || len(f.queries) != 2 {
		t.Errorf("look = %+v err = %v queries = %v", look, err, f.queries)
	}

	// The ISRC decides alone, and with Sample the title search runs for Searched.
	d := hardstyle.Details{Date: "2026-10-29", ISRC: "NL1"}
	for _, sample := range []bool{false, true} {
		f = &fakeSearch{isrc: map[string][]string{"NL1": {item("I", "Different Name", 200000)}}, items: []string{item("S", "Song", 200000)}}
		look, err = f.client(t).FindTrack(ctx, site, d, MatchOptions{Sample: sample})
		if err != nil || !slices.Equal(look.URIs, uris("I")) || !look.ByISRC || look.Sampled != sample {
			t.Errorf("sample=%v: look = %+v err = %v", sample, look, err)
		}
		wantQueries, wantSearched := 1, 0
		if sample {
			wantQueries, wantSearched = 2, 1
		}
		if len(f.queries) != wantQueries || len(look.Searched) != wantSearched {
			t.Errorf("sample=%v: queries = %v searched = %v", sample, f.queries, look.Searched)
		}
	}
}
