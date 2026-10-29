package match

import (
	"hardstylesync/internal/hardstyle"
	"testing"
)

// fakeSpotifyTrack is what the matcher needs to know about a track.
type fakeSpotifyTrack struct {
	name    string
	artists []string
}

func TestMatches(t *testing.T) {
	cases := []struct {
		site hardstyle.Track
		sp   fakeSpotifyTrack
		want bool
	}{
		{hardstyle.Track{Title: "Flatline", Artist: "Act of Rage, Spitnoise and Nolz"}, fakeSpotifyTrack{name: "Flatline - Extended Mix", artists: []string{"Act of Rage", "Spitnoise"}}, true},
		{hardstyle.Track{Title: "Drugs (Crypsis Remix)", Artist: "Ran-D and Act of Rage"}, fakeSpotifyTrack{name: "Drugs - Crypsis Remix", artists: []string{"Ran-D", "Act of Rage", "Crypsis"}}, true},
		{hardstyle.Track{Title: "Drugs (Crypsis Remix)", Artist: "Ran-D"}, fakeSpotifyTrack{name: "Drugs", artists: []string{"Ran-D"}}, false}, // original is not the remix
		{hardstyle.Track{Title: "Legacy", Artist: "Act of Rage"}, fakeSpotifyTrack{name: "Legacy", artists: []string{"Someone Else"}}, false},
		{hardstyle.Track{Title: "Can't Take This (Rejecta Remix)", Artist: "Act of Rage"}, fakeSpotifyTrack{name: "Can't Take This (Rejecta Remix)", artists: []string{"Act Of Rage"}}, true},
		{hardstyle.Track{Title: "Song", Artist: "A & B"}, fakeSpotifyTrack{name: "Song (Radio Edit)", artists: []string{"B"}}, true},
	}
	for _, c := range cases {
		if got := Matches(c.site, c.sp.name, c.sp.artists); got != c.want {
			t.Errorf("Matches(%+v, %+v) = %v, want %v", c.site, c.sp, got, c.want)
		}
	}
}

// Run with HS_LIVE=1 to check the parser against the real site.

func TestFold(t *testing.T) {
	for in, want := range map[string]string{
		"Juliëx": "Juliex", "GRIVØ": "GRIVO", "José del Barrio": "Jose del Barrio", "Æsthetic": "AEsthetic", "Straße": "Strasse",
		"Beyoncé": "Beyonce", "Łódź": "Lodz", "plain": "plain", "Ñengo Flow": "Nengo Flow",
	} {
		if got := Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSpecialLettersInArtistNames(t *testing.T) {
	cases := []struct {
		site hardstyle.Track
		sp   fakeSpotifyTrack
		want bool
	}{
		{hardstyle.Track{Title: "Lose Control", Artist: "Juliex"}, fakeSpotifyTrack{name: "Lose Control", artists: []string{"Juliëx"}}, true},
		{hardstyle.Track{Title: "HARDCORE", Artist: "GRIVO"}, fakeSpotifyTrack{name: "HARDCORE", artists: []string{"GRIVØ"}}, true},
		{hardstyle.Track{Title: "Song", Artist: "José Del Barrio"}, fakeSpotifyTrack{name: "Song", artists: []string{"Jose del Barrio"}}, true},
		{hardstyle.Track{Title: "Song", Artist: "Jose Del Barrio"}, fakeSpotifyTrack{name: "Song", artists: []string{"JoseDelBarrio"}}, true},    // spaces
		{hardstyle.Track{Title: "Song", Artist: "Act of Rage"}, fakeSpotifyTrack{name: "Song", artists: []string{"Act of Rage Official"}}, true}, // beginning of the name
		{hardstyle.Track{Title: "Song", Artist: "Dual Damage"}, fakeSpotifyTrack{name: "Song", artists: []string{"Dual Damag"}}, true},           // one letter
		{hardstyle.Track{Title: "Song", Artist: "Juliex"}, fakeSpotifyTrack{name: "Song", artists: []string{"Someone Else"}}, false},
		{hardstyle.Track{Title: "Song", Artist: "Rox"}, fakeSpotifyTrack{name: "Song", artists: []string{"Roxy Music"}}, false}, // too short for a prefix
		{hardstyle.Track{Title: "Lose Control", Artist: "Juliex"}, fakeSpotifyTrack{name: "Lose It", artists: []string{"Juliëx"}}, false},
		// The artist line with "and" in it, as hardstyle.com writes it, and the two names as Spotify has them.
		{hardstyle.Track{Title: "Gabber I'm G", Artist: "Double Trouble and Dedicated"}, fakeSpotifyTrack{name: "Gabber I'm A G", artists: []string{"Double Trouble", "Dedicated"}}, true},
	}
	for _, c := range cases {
		if got := Matches(c.site, c.sp.name, c.sp.artists); got != c.want {
			t.Errorf("Matches(%+v, %+v) = %v, want %v", c.site, c.sp, got, c.want)
		}
	}
}

func TestTitleMatchKinds(t *testing.T) {
	everything := hardstyle.Track{Title: "Everything Is Looking Good", Version: "Fiesta Mix"}
	plain := hardstyle.Track{Title: "Everything Is Looking Good"}
	cases := []struct {
		site hardstyle.Track
		name string
		want Kind
	}{
		{everything, "Everything Is Looking Good - Fiesta Mix", Exact},
		{everything, "Everything Is Looking Good (Fiesta Mix)", Exact},
		{everything, "Everything Is Looking Good", Base},
		{everything, "Everything Is Looking Good - Happy Mix", Version}, // the other version of the song
		{plain, "Everything Is Looking Good - Happy Mix", Version},      // a version the site does not name
		{plain, "Everything Is Looking Good (Some Remix)", Version},
		{plain, "Everything Is Looking Good", Exact},
		{plain, "Everything Is Looking Good Part 2", None}, // another song
		{plain, "Everything Is Looking Goof", Exact},       // one letter off, long enough title
		{plain, "Everything", None},
		{hardstyle.Track{Title: "Drugs (Crypsis Remix)"}, "Drugs - Crypsis Remix", Exact},
		{hardstyle.Track{Title: "Drugs (Crypsis Remix)"}, "Drugs", None},
		{hardstyle.Track{Title: "Flatline", Version: "Extended Mix"}, "Flatline", Exact}, // format words do not count
		{hardstyle.Track{Title: "Lose"}, "Lose Control", None},
		{hardstyle.Track{Title: "Juliëx Song"}, "Juliex Song", Exact},
		// One site writes the article, the other does not.
		{hardstyle.Track{Title: "Gabber I'm G"}, "Gabber I'm A G", Exact},
		{hardstyle.Track{Title: "Gabber I'm A G"}, "Gabber I'm G", Exact},
		{hardstyle.Track{Title: "The Rush"}, "Rush", Exact},
		{hardstyle.Track{Title: "A New Day"}, "New Day - Extended Mix", Exact},
		{hardstyle.Track{Title: "Lose Control"}, "Lose It", None}, // an article alone does not make a match
	}
	for _, c := range cases {
		if got := Title(c.site, c.name); got != c.want {
			t.Errorf("Title(%+v, %q) = %v, want %v", c.site, c.name, got, c.want)
		}
	}
}

func TestSearchArtists(t *testing.T) {
	for in, want := range map[string]string{
		"Double Trouble and Dedicated": "Double Trouble Dedicated", // the joiners spoil the search
		"D-Block & S-te-Fan":           "D-Block S-te-Fan",
		"Ran-D, Adaro":                 "Ran-D Adaro",
		"Act of Rage":                  "Act of Rage", // no joiner
		"X-Pander":                     "X-Pander",    // a name with an x stays whole
	} {
		if got := SearchArtists(in); got != want {
			t.Errorf("SearchArtists(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDateFits(t *testing.T) {
	cases := []struct {
		site, spotify, precision string
		want                     bool
	}{
		{"2026-10-29", "2026-10-29", "day", true},
		{"2026-10-29", "2026-10-30", "day", true},
		{"2026-10-29", "2026-10-22", "day", true},  // exactly 7 days
		{"2026-10-29", "2026-10-21", "day", false}, // 8 days
		{"2026-10-29", "2020-01-01", "day", false},
		{"2026-10-29", "2026-10", "month", true},
		{"2026-10-02", "2026-09", "month", true}, // two days after the end of September
		{"2026-10-29", "2026-08", "month", false},
		{"2026-10-29", "2026", "year", true},
		{"2026-10-29", "2025", "year", false},
		{"", "2020-01-01", "day", true}, // date of the site unknown: nothing to compare
		{"2026-10-29", "", "day", true}, // Spotify gives no date
		{"2026-10-29", "garbage", "day", true},
	}
	for _, c := range cases {
		if got, _ := Date(c.site, c.spotify, c.precision, 7); got != c.want {
			t.Errorf("Date(%q, %q, %q) = %v, want %v", c.site, c.spotify, c.precision, got, c.want)
		}
	}
}
