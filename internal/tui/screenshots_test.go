package tui

// This file renders the screens of the interface into ANSI files for the screenshots in docs/screenshots.
// It only runs when SCREENSHOT_DIR is set, so a normal `go test ./...` does nothing here:
//
//	SCREENSHOT_DIR=../docs/screenshots go test ./internal/tui -run TestScreenshots -count=1
//
// Every file is then turned into a PNG with the ansi2png helper described in docs/screenshots/README.md.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"hardstylesync/internal/event"
	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/i18n"
	"hardstylesync/internal/spotify"
	"hardstylesync/internal/sync"
)

const shotWidth, shotHeight = 112, 38

// shotNow is the fixed clock of the screenshots.
var shotNow = time.Date(2026, 10, 29, 14, 32, 8, 0, time.Local)

// The releases of the demo list: title, version (the second line on hardstyle.com) and artist.
var demoTracks = []struct{ title, version, artist string }{
	{"Lose Control", "Extended Mix", "Sub Zero Project"},
	{"Fading Lights", "", "D-Block & S-te-Fan"},
	{"Gravity", "Extended Mix", "Headhunterz"},
	{"Together", "", "Da Tweekaz & Coone"},
	{"No Sleep", "", "Brennan Heart"},
	{"Echoes", "Remix", "Wildstylez"},
	{"Dreamstate", "", "Phuture Noize"},
	{"Rise Again", "Extended Mix", "Sound Rush"},
	{"Blackout", "", "Rebelion"},
	{"Underground", "", "Angerfist"},
	{"Harder", "", "N-Vitral"},
	{"Tempo", "", "Sefa"},
	{"French Kiss", "", "Dr. Peacock"},
	{"Nightdrive", "", "Billx"},
	{"Adrenaline", "Radio Edit", "Vertile"},
	{"Fired Up", "", "Aversion"},
	{"Halfway There", "", "Rooler & Sickmode"},
	{"Skyline", "Extended Mix", "Dimitri K"},
	{"Break The Silence", "", "B-Front"},
	{"Wide Awake", "", "Ran-D"},
	{"Hold On", "Acoustic", "Adaro"},
	{"Stone Cold", "", "E-Force"},
	{"Higher", "Extended Mix", "Zatox"},
	{"Old School", "", "TNT"},
	{"Madness", "", "Crypsis"},
	{"New Era", "", "D-Sturb"},
	{"Afterglow", "Remix", "Keltek"},
	{"Fire And Ice", "", "Endymion"},
	{"Loud", "", "Warface"},
	{"Freefall", "Extended Mix", "Act of Rage"},
	{"Miracle", "", "B-Freqz"},
	{"The Other Side", "", "Rejecta"},
	{"Rebel", "", "Malice"},
	{"Let It Go", "", "Korsakoff"},
	{"Raw Energy", "", "Unresolved"},
	{"Nightmare", "", "Deadly Guns"},
	{"Lost In Time", "Extended Mix", "Udex"},
	{"One More Time", "", "Never Surrender"},
	{"Rave", "", "The Dark Horror"},
	{"Into The Night", "", "Furyan"},
}

// demoGenres are the genres of the run.
var demoGenres = []string{"Hardstyle", "Hardcore", "Uptempo"}

// shotModel builds a model in the middle of a run, with the events already applied.
func shotModel() *model {
	opts := Options{
		Genres: demoGenres, Pages: 4, MaxTracks: 500, MaxSearches: 100,
		RequestDelay: 800 * time.Millisecond, ContinueDelay: 10 * time.Minute,
		Every: 24 * time.Hour, Watch: 15 * time.Minute,
		Apps: func() []spotify.AppStatus {
			return []spotify.AppStatus{
				{ClientID: "app1", InUse: true},
				{ClientID: "app2"},
				{ClientID: "app3", BlockedUntil: shotNow.Add(41 * time.Minute)},
			}
		},
	}
	m := newModel(context.Background(), opts)
	m.cancel = func() {}
	m.now = shotNow
	m.running = true
	m.phase = event.PhaseSearch
	m.follow = false
	m.done, m.total = 47, 120
	m.requests = 53
	m.playlist = event.PlaylistStats{
		Count:  487,
		Oldest: time.Date(2025, 11, 3, 0, 0, 0, 0, time.UTC),
		Newest: time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC),
	}
	m.hasList = true

	tracks := make([]hardstyle.Track, 120)
	for i := range tracks {
		d := demoTracks[i%len(demoTracks)]
		version := d.version
		if i >= len(demoTracks) {
			// The list is longer than the pool; keep every entry different but hidden below the fold anyway.
			version = "Club Mix"
		}
		tracks[i] = hardstyle.Track{
			Title: d.title, Version: version, Artist: d.artist,
			Genre: demoGenres[i%len(demoGenres)],
		}
	}
	m.Update(eventMsg(event.Event{Kind: event.EvReleases, Releases: tracks}))

	statuses := map[int]event.Status{}
	add := func(from, to int, s event.Status) {
		for i := from; i < to; i++ {
			statuses[i] = s
		}
	}
	add(0, 6, event.StatusNew)
	add(6, 18, event.StatusAdded)
	add(18, 34, event.StatusInPlaylist)
	statuses[35] = event.StatusNotFound
	statuses[36] = event.StatusMismatch
	statuses[38] = event.StatusNotFound
	add(39, 47, event.StatusNew)
	// Everything from 47 on waits: the search limit of the run is not reached yet, but the run has to end somewhere.

	for i := 0; i < 47; i++ {
		s, ok := statuses[i]
		if !ok {
			s = event.StatusInPlaylist
		}
		ev := event.Event{Kind: event.EvItem, Index: i, Status: s, Date: shotDate(i)}
		if s != event.StatusNotFound && s != event.StatusMismatch {
			ev.URI = fmt.Sprintf("spotify:track:%06d", i+1)
		}
		switch i {
		case 3:
			ev.Note = "Version on Spotify: \"Together (Extended Mix)\""
		case 35:
			ev.Note = "Date differs by 62 days"
		case 36:
			ev.Note = "Only a live version on Spotify"
		case 38:
			ev.Note = "No confident match"
		}
		m.Update(eventMsg(ev))
	}

	for _, line := range []struct {
		text string
		err  bool
	}{
		{"Run started", false},
		{"hardstyle.com · Hardstyle: 120 releases on 4 pages", false},
		{"hardstyle.com · Hardcore: 118 releases on 4 pages", false},
		{"hardstyle.com · Uptempo: 96 releases on 4 pages", false},
		{"Playlist read: 487 tracks, snapshot up to date", false},
		{"Spotify app #1 in use", false},
		{"Found: Sub Zero Project – Lose Control", false},
		{"Found: D-Block & S-te-Fan – Fading Lights", false},
		{"Already in the playlist: Echoes (Wildstylez)", false},
		{"Not on Spotify: Udex – Lost In Time", true},
		{"Added: Headhunterz – Gravity", false},
		{"Date differs by 62 days: Adaro – Hold On", true},
	} {
		m.logs = append(m.logs, logLine{at: shotNow.Add(-3*time.Minute + time.Duration(len(m.logs))*7*time.Second), text: line.text, err: line.err})
	}

	m.selected, m.offset = 12, 0
	return m
}

// shotDate is the release date on hardstyle.com of the release at index i.
func shotDate(i int) string {
	return shotNow.AddDate(0, 0, -i/3).Format("2006-01-02")
}

// writeShot renders the current view of the model into SCREENSHOT_DIR.
func writeShot(t *testing.T, dir, name string, m *model) {
	t.Helper()
	m.Update(tea.WindowSizeMsg{Width: shotWidth, Height: shotHeight})
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".ans")
	if err := os.WriteFile(path, []byte(m.View()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", path)
}

func TestScreenshots(t *testing.T) {
	dir := os.Getenv("SCREENSHOT_DIR")
	if dir == "" {
		t.Skip("set SCREENSHOT_DIR to render the screenshots")
	}
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })
	oldVersion := Version
	Version = "0.1.0"
	t.Cleanup(func() { Version = oldVersion })
	if err := i18n.SetLanguage("en"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(i18n.Reset)

	// 00 · the intro
	intro := shotModel()
	intro.splash = true
	intro.splashFrame = 22 // 1.76 s: the logos are in, the kicks run
	writeShot(t, dir, "00-intro", intro)

	// 01 · a running sync
	writeShot(t, dir, "01-run", shotModel())

	// 02 · a genre filter
	genre := shotModel()
	genre.filter = "Hardcore"
	genre.selected, genre.offset = 4, 0
	writeShot(t, dir, "02-genre", genre)

	// 03 · the end of a run
	done := shotModel()
	done.running = false
	done.phase = event.PhaseDone
	done.done, done.total = 120, 120
	done.requests = 86
	done.result = &sync.Result{
		Added: 9, Removed: 4, Deferred: 6,
		Unmatched: make([]hardstyle.Track, 2),
	}
	done.lastRun = shotNow.Add(-2 * time.Minute)
	done.nextRun = shotNow.Add(23*time.Hour + 58*time.Minute)
	done.nextWhy = "new tracks"
	done.logs = append(done.logs,
		logLine{at: shotNow.Add(-2 * time.Minute), text: "Playlist updated: 9 added, 4 removed", err: false},
		logLine{at: shotNow.Add(-2 * time.Minute), text: "Next run in 23 h 58 min (new tracks)", err: false},
	)
	writeShot(t, dir, "03-done", done)

	// 04 · a dry run
	dry := shotModel()
	dry.dryRun = true
	dry.selected = 1
	for i := range dry.items {
		if dry.items[i].status == event.StatusAdded {
			dry.items[i].status = event.StatusNew // a dry run adds nothing
		}
	}
	writeShot(t, dir, "04-dry-run", dry)

	// 05 · the enlarged log
	logs := shotModel()
	logs.logView = true
	for i := 0; i < 18; i++ {
		logs.logs = append(logs.logs, logLine{
			at:   shotNow.Add(-90*time.Second + time.Duration(i)*4*time.Second),
			text: fmt.Sprintf("Looking up %d/120: %s – %s", 48+i, demoTracks[(47+i)%len(demoTracks)].artist, demoTracks[(47+i)%len(demoTracks)].title),
		})
	}
	writeShot(t, dir, "05-log", logs)

	// 06 · the help
	help := shotModel()
	help.help = true
	writeShot(t, dir, "06-help", help)

	// 07 · Spotify is blocking the app
	blocked := shotModel()
	blocked.running = false
	blocked.phase = event.PhaseDone
	blocked.err = &spotify.RateLimitError{RetryAfter: 43 * time.Minute, Until: shotNow.Add(43 * time.Minute)}
	blocked.lastRun = shotNow.Add(-time.Minute)
	blocked.nextRun = shotNow.Add(44 * time.Minute)
	blocked.nextWhy = "Spotify block"
	blocked.logs = append(blocked.logs,
		logLine{at: shotNow.Add(-time.Minute), text: "Spotify is limiting the app (rate limit), possible again in 43 min", err: true},
		logLine{at: shotNow.Add(-time.Minute), text: "Next run in 44 min (Spotify block)", err: false},
	)
	writeShot(t, dir, "07-blocked", blocked)

	// 08 · a narrow terminal: the list stands alone
	narrow := shotModel()
	narrow.width, narrow.height = 82, 26
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "08-narrow.ans")
	if err := os.WriteFile(path, []byte(narrow.View()), 0o644); err != nil {
		t.Fatal(err)
	}
}
