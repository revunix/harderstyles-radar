package eval

import (
	"hardstylesync/internal/cache"
	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/spotify"
	"path/filepath"
	"strings"
	"testing"
)

func stored(id, name string, artists ...string) cache.StoredTrack {
	return cache.StoredTrack{URI: "spotify:track:" + id, Name: name, Artists: artists, ReleaseDate: "2026-10-29", DatePrecision: "day"}
}

func TestEvaluate(t *testing.T) {
	c, err := cache.Load(filepath.Join(t.TempDir(), "cache.json"))
	if err != nil {
		t.Fatal(err)
	}
	song := cache.StoredRelease{Title: "Song", Artist: "A", Date: "2026-10-29"}
	// add keeps a release with what its search found; truth by ISRC when isrc is set.
	add := func(id string, r cache.StoredRelease, isrc string, searched ...cache.StoredTrack) hardstyle.Track {
		tr := hardstyle.Track{ID: id, Title: r.Title, Artist: r.Artist}
		c.SetDetails(tr, hardstyle.Details{Date: r.Date, ISRC: "X" + id})
		if isrc != "" {
			c.SetFound(tr, cache.Found{URIs: []string{"spotify:track:" + isrc}, ByISRC: true})
		}
		c.SetSearched(tr, r, searched)
		return tr
	}

	add("correct", song, "S", stored("S", "Song", "A"))
	wrong := add("wrong", song, "", stored("W", "Song", "A"), stored("F", "Totally Different", "Z"))
	c.Force(wrong, "spotify:track:F")
	refused := add("refused", song, "T", stored("R", "Song", "A"))
	c.Reject(refused, "spotify:track:R")
	add("uncertain", song, "E", stored("RE", "Song - Radio Edit", "A"), stored("E", "Song - Extended Mix", "A"))
	missed := add("missed", song, "", stored("M", "Song - Crypsis Remix", "A"))
	c.Force(missed, "spotify:track:M")
	add("unreachable", song, "U", stored("X", "Other", "B"))
	add("unlabeled", song, "", stored("S2", "Song", "A"))
	// Without the stored search the release is not a case.
	c.SetDetails(hardstyle.Track{ID: "nosearch"}, hardstyle.Details{Date: "2026-10-29"})

	r := Evaluate(c, spotify.MatchOptions{Tolerance: 7})
	want := Counts{Correct: 1, Wrong: 2, Uncertain: 1, Missed: 1, Unreachable: 1, Unlabeled: 1, Reachable: 4}
	if r.Counts != want {
		t.Errorf("counts = %+v, want %+v", r.Counts, want)
	}
	if p, ok := r.Precision(); !ok || p != 1.0/3 {
		t.Errorf("precision = %v %v, want 1/3", p, ok)
	}
	if rc, ok := r.Recall(); !ok || rc != 0.25 {
		t.Errorf("recall = %v %v, want 1/4", rc, ok)
	}
	if len(r.Cases) != 7 || r.Cases[0].Class != Correct || r.Cases[6].Class != Unlabeled {
		t.Errorf("cases = %+v", r.Cases)
	}

	defaults, lenient := 0, false
	for _, p := range r.Sweep {
		if p.Default {
			defaults++
			if p.MinScore != spotify.DefaultMinScore || p.MinMargin != spotify.DefaultMinMargin || p.Counts != r.Counts {
				t.Errorf("default point = %+v, want the counts of the defaults %+v", p, r.Counts)
			}
		}
		// A lower bar takes the remix alone, which is the truth of one release.
		if p.MinScore == 0.5 && p.Correct == r.Correct+1 {
			lenient = true
		}
	}
	if defaults != 1 || !lenient {
		t.Errorf("sweep = %+v: want one default point and a lower bar that takes the remix", r.Sweep)
	}
	if s := r.String(); !strings.Contains(s, "*") || !strings.Contains(s, "spotify:track:F") {
		t.Errorf("report misses the default mark or a wrong case:\n%s", s)
	}
}

func TestEvaluateEmpty(t *testing.T) {
	c, err := cache.Load(filepath.Join(t.TempDir(), "cache.json"))
	if err != nil {
		t.Fatal(err)
	}
	r := Evaluate(c, spotify.MatchOptions{})
	if _, ok := r.Precision(); ok || len(r.Cases) != 0 || r.String() == "" {
		t.Errorf("empty report = %+v", r)
	}
}
