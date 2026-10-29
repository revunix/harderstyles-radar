package sync

import (
	"context"
	"errors"
	"fmt"
	"hardstylesync/internal/cache"
	"hardstylesync/internal/config"
	"hardstylesync/internal/event"
	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/i18n"
	"hardstylesync/internal/spotify"
	"strings"
	"testing"
	"time"
)

func TestSchedule(t *testing.T) {
	s := Schedule{Every: 24 * time.Hour, Continue: 10 * time.Minute}
	limit := spotify.NewRateLimitError(3 * time.Hour)
	cases := []struct {
		name     string
		s        Schedule
		res      Result
		err      error
		failures int
		min, max time.Duration
		why      string
	}{
		{"complete run waits for the regular time", s, Result{}, nil, 0, 24 * time.Hour, 24 * time.Hour, "geplanter Lauf"},
		{"complete run without regular runs ends", Schedule{Continue: time.Minute}, Result{}, nil, 0, 0, 0, "geplanter Lauf"},
		{"work left over goes on soon", s, Result{Deferred: 40}, nil, 0, 10 * time.Minute, 10 * time.Minute, "40 warten"},
		{"also without regular runs", Schedule{Continue: 10 * time.Minute}, Result{Deferred: 1}, nil, 0, 10 * time.Minute, 10 * time.Minute, "1 warten"},
		{"a dry run does not carry on", Schedule{Every: time.Hour, Continue: time.Minute, DryRun: true}, Result{Deferred: 5}, nil, 0, time.Hour, time.Hour, "geplanter Lauf"},
		{"rate limit: when Spotify lets the app in", s, Result{}, fmt.Errorf("x: %w", limit), 0, 3*time.Hour + 30*time.Second, 3*time.Hour + 2*time.Minute, "Spotify-Sperre"},
		{"error: try again", s, Result{}, errors.New("boom"), 1, 10 * time.Minute, 10 * time.Minute, "nach einem Fehler"},
		{"errors in a row: longer pauses", s, Result{}, errors.New("boom"), 3, 40 * time.Minute, 40 * time.Minute, "nach einem Fehler"},
		{"errors in a row: at most an hour", s, Result{}, errors.New("boom"), 9, time.Hour, time.Hour, "nach einem Fehler"},
		{"cancelled by the user: stop", s, Result{Deferred: 3}, context.Canceled, 0, 0, 0, ""},
	}
	for _, c := range cases {
		got, why, _ := c.s.Next(c.res, c.err, c.failures, 0)
		if got < c.min || got > c.max || why != c.why {
			t.Errorf("%s: got %s %q, want %s..%s %q", c.name, got, why, c.min, c.max, c.why)
		}
	}
}

func TestPlaylistDescription(t *testing.T) {
	day := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		cfg  config.Config
		want string
	}{
		{"general list", config.Config{Genres: []string{hardstyle.AnyGenre}, MaxTracks: 500},
			"Daily updated. The latest all-genre releases from hardstyle.com, in the same order as on the site (newest first), max. 500 tracks. Last update: 29 Oct 2026"},
		{"one genre", config.Config{Genres: []string{"Hardstyle"}, MaxTracks: 300},
			"Daily updated. The latest Hardstyle releases from hardstyle.com, in the same order as on the site (newest first), max. 300 tracks. Last update: 29 Oct 2026"},
		{"several genres", config.Config{Genres: []string{"Hardstyle", "Uptempo"}, MaxTracks: 500, Description: "From hardstyle.com: {source}. Updated daily, last on {date} ({max})."},
			"From hardstyle.com: Hardstyle, Uptempo. Updated daily, last on 29 Oct 2026 (500)."},
	}
	for _, c := range cases {
		if got := playlistDescription(c.cfg, day); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
	if got := playlistDescription(config.Config{Genres: []string{hardstyle.AnyGenre}, MaxTracks: 500}, day); len([]rune(got)) > maxDescription {
		t.Errorf("default text is %d characters, Spotify takes %d", len([]rune(got)), maxDescription)
	}
	long := playlistDescription(config.Config{Description: strings.Repeat("x", 500)}, day)
	if len([]rune(long)) != maxDescription || !strings.HasSuffix(long, "…") {
		t.Errorf("a long text must be cut to %d characters, got %d", maxDescription, len([]rune(long)))
	}
}

func TestDescriptionIsWrittenOncePerDay(t *testing.T) {
	f := &fakeSpotify{}
	sp := newFakeClient(t, f)
	c, path := newCache(t)
	cfg := config.Config{PlaylistID: "p", Genres: []string{hardstyle.AnyGenre}, MaxTracks: 500}
	var logged []string
	emit := func(ev event.Event) { logged = append(logged, ev.Msg) }
	day1 := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)

	updateDescription(context.Background(), sp, cfg, c, day1, emit)
	if f.descriptionWrites != 1 || !strings.Contains(f.description, "Last update: 29 Oct 2026") || !strings.Contains(f.description, "hardstyle.com") {
		t.Fatalf("writes=%d description=%q", f.descriptionWrites, f.description)
	}

	// Again on the same day, also in a new process: nothing to do, no request.
	c, _ = cache.Load(path)
	updateDescription(context.Background(), sp, cfg, c, day1.Add(10*time.Hour), emit)
	if f.descriptionWrites != 1 {
		t.Errorf("the same text must not be written again (%d writes)", f.descriptionWrites)
	}

	updateDescription(context.Background(), sp, cfg, c, day1.AddDate(0, 0, 1), emit)
	if f.descriptionWrites != 2 || !strings.Contains(f.description, "Last update: 30 Oct 2026") {
		t.Errorf("next day: writes=%d description=%q", f.descriptionWrites, f.description)
	}
}

func TestDescriptionCanBeSwitchedOffAndFailsSoftly(t *testing.T) {
	f := &fakeSpotify{}
	sp := newFakeClient(t, f)
	c, _ := newCache(t)
	var logged []string
	emit := func(ev event.Event) { logged = append(logged, ev.Msg) }
	day := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)

	updateDescription(context.Background(), sp, config.Config{PlaylistID: "p", Description: "OFF"}, c, day, emit)
	if f.descriptionWrites != 0 {
		t.Error("PLAYLIST_DESCRIPTION=off must leave the description alone")
	}

	f.failDescription = true
	updateDescription(context.Background(), sp, config.Config{PlaylistID: "p", Genres: []string{hardstyle.AnyGenre}, MaxTracks: 500}, c, day, emit)
	if len(logged) != 1 || !strings.Contains(logged[0], "nicht geändert") || c.Description("p") != "" {
		t.Errorf("a failure must be logged and not remembered: log=%v remembered=%q", logged, c.Description("p"))
	}
}

func TestScheduleWithTheService(t *testing.T) {
	s := Schedule{Watch: 15 * time.Minute, Continue: 10 * time.Minute, Every: 24 * time.Hour}
	wait, why, quick := s.Next(Result{}, nil, 0, time.Hour)
	if wait != 15*time.Minute || !quick || why != "Prüfung auf neue Tracks" {
		t.Errorf("after a run: %s %q quick=%v, want a check in 15 minutes", wait, why, quick)
	}
	// The daily full run is due soon: it comes instead of the check.
	wait, why, quick = s.Next(Result{}, nil, 0, 23*time.Hour+50*time.Minute)
	if wait != 10*time.Minute || quick || why != "geplanter Lauf" {
		t.Errorf("full run due: %s %q quick=%v", wait, why, quick)
	}
	// Leftover work goes before the check, and it is a full run.
	wait, _, quick = s.Next(Result{Deferred: 5}, nil, 0, time.Hour)
	if wait != 10*time.Minute || quick {
		t.Errorf("leftover work: %s quick=%v", wait, quick)
	}
	// Without Every only the checks: no regular full run.
	only := Schedule{Watch: 15 * time.Minute}
	if wait, _, quick := only.Next(Result{}, nil, 0, 1000*time.Hour); wait != 15*time.Minute || !quick {
		t.Errorf("only checks: %s quick=%v", wait, quick)
	}
	// Neither: nothing follows.
	if wait, _, _ := (Schedule{}).Next(Result{}, nil, 0, 0); wait != 0 {
		t.Errorf("no service: %s", wait)
	}
}

func TestEnglishMessages(t *testing.T) {
	withLanguage(t, "en")
	if got := i18n.Duration(3*time.Hour + 5*time.Minute); got != "3 h 5 min" {
		t.Errorf("humanDuration = %q", got)
	}
	if got := i18n.Duration(7 * time.Minute); got != "7 min" {
		t.Errorf("humanDuration = %q", got)
	}
	err := spotify.NewRateLimitError(25 * time.Hour)
	if msg := err.Error(); !strings.Contains(msg, "rate limit") || !strings.Contains(msg, "25 h 0 min") {
		t.Errorf("rate limit message = %q", msg)
	}
	s := Schedule{Every: time.Hour, Continue: time.Minute}
	if _, why, _ := s.Next(Result{Deferred: 4}, nil, 0, 0); why != "4 waiting" {
		t.Errorf("why = %q", why)
	}
	if _, why, _ := s.Next(Result{}, errors.New("x"), 1, 0); why != "after an error" {
		t.Errorf("why = %q", why)
	}
	if got := reasonText(spotify.ReasonDate, "2026-09-25", "2026-10-23"); got != "Date differs: Spotify 2026-09-25, hardstyle.com 2026-10-23 (28 days)" {
		t.Errorf("reasonText = %q", got)
	}
	// A month or a year is no day to count.
	if got := reasonText(spotify.ReasonDate, "2026-09", "2026-10-23"); got != "Date differs: Spotify 2026-09, hardstyle.com 2026-10-23" {
		t.Errorf("reasonText = %q", got)
	}
	if got := reasonText(spotify.ReasonNotFound, "", ""); got != "not found on Spotify" {
		t.Errorf("reasonText = %q", got)
	}
	if got := hardstyle.GenreLabel(hardstyle.AnyGenre); got != "all genres" {
		t.Errorf("genreLabel = %q", got)
	}
	if got := i18n.Date(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)); got != "2 Jan 2026" {
		t.Errorf("formatDate = %q", got)
	}
}

func TestEnglishLogOfARun(t *testing.T) {
	withLanguage(t, "en")
	f := &fakeSpotify{missing: map[string]bool{"T2": true}}
	f.setPlaylist("A")
	sp := newFakeClient(t, f)
	c, _ := newCache(t)
	var logs []string
	emit := func(ev event.Event) {
		if ev.Kind == event.EvLog {
			logs = append(logs, ev.Msg)
		}
	}
	if _, err := Sync(context.Background(), sp, "p", site("T1", "T2"), opts(nil), c, emit); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(logs, "\n")
	for _, want := range []string{"Playlist read: 1 tracks", "releases:", "Not on Spotify: T2", "not found on Spotify", "tracks inserted at the top"} {
		if !strings.Contains(all, want) {
			t.Errorf("log lacks %q:\n%s", want, all)
		}
	}
	for _, w := range []string{"Nicht bei", "gelesen", "eingefügt", "Zwischenspeicher", "Releases:"} {
		if strings.Contains(all, w) {
			t.Errorf("English log has %q:\n%s", w, all)
		}
	}
}

func TestOldGermanReasonsInTheCacheStillWork(t *testing.T) {
	for _, old := range []string{"Datum weicht ab: Spotify 2020-01-01, hardstyle.com 2026-10-29", "Date differs: x", spotify.ReasonDate} {
		if normalizeReason(old) != spotify.ReasonDate {
			t.Errorf("%q must count as a date mismatch", old)
		}
	}
	for _, old := range []string{"nicht bei Spotify gefunden", "", "whatever"} {
		if normalizeReason(old) != spotify.ReasonNotFound {
			t.Errorf("%q must count as not found", old)
		}
	}

	// An entry of an older version (German text, no date of the Spotify track) shows as a date mismatch.
	c, _ := newCache(t)
	tr := site("T1")[0]
	c.SetDetails(tr, hardstyle.Details{Date: "2026-10-29"})
	c.SetMissing(tr, "Datum weicht ab: Spotify 2020-01-01, hardstyle.com 2026-10-29", "2020-01-01")
	known, err := resolveAll(context.Background(), nil, []hardstyle.Track{tr}, nil, opts(nil), c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if statusOf(known[0], nil) != event.StatusMismatch || !strings.Contains(known[0].note(), "hardstyle.com 2026-10-29") {
		t.Errorf("status=%v note=%q", statusOf(known[0], nil), known[0].note())
	}
}

// withLanguage switches the language for one test.
func withLanguage(t *testing.T, name string) {
	t.Helper()
	if err := i18n.SetLanguage(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(i18n.Reset)
}
