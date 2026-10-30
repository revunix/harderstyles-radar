package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"hardstylesync/internal/corrections"
	"hardstylesync/internal/event"
	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/i18n"
	"hardstylesync/internal/spotify"
	"hardstylesync/internal/sync"
)

var testGenres = []string{"Hardstyle", "Hardcore", "Uptempo"}

// demoModel returns a model in the middle of a run, as if the events had come in.
func demoModel(width, height, releases int) *model {
	m := newModel(context.Background(), Options{Genres: testGenres, Pages: 4, MaxTracks: 500, Every: 24 * time.Hour})
	m.cancel = func() {}
	m.running = true
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})

	tracks := make([]hardstyle.Track, releases)
	for i := range tracks {
		tracks[i] = hardstyle.Track{Title: fmt.Sprintf("Track Nummer %d (Extended Mix)", i+1), Artist: fmt.Sprintf("Artist %d & Guest", i%7), Genre: testGenres[i%3]}
	}
	apply := func(ev event.Event) { m.Update(eventMsg(ev)) }
	apply(event.Event{Kind: event.EvPlaylist, Playlist: event.PlaylistStats{Count: 312, Oldest: time.Date(2026, 1, 12, 0, 0, 0, 0, time.UTC), Newest: time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)}})
	apply(event.Event{Kind: event.EvReleases, Releases: tracks})
	apply(event.Event{Kind: event.EvLog, Msg: "hardstyle.com Hardstyle: 120 Releases"})
	statuses := []event.Status{event.StatusNew, event.StatusInPlaylist, event.StatusNotFound}
	for i := 0; i < releases*2/3; i++ {
		apply(event.Event{Kind: event.EvItem, Index: i, Status: statuses[i%3], URI: fmt.Sprintf("spotify:track:abc%d", i), Date: "2026-10-29"})
	}
	apply(event.Event{Kind: event.EvPhase, Phase: event.PhaseSearch, Done: releases * 2 / 3, Total: releases})
	return m
}

func TestViewFitsTheWindow(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })

	for _, size := range [][2]int{{200, 60}, {160, 50}, {120, 36}, {100, 30}, {99, 30}, {80, 24}, {60, 20}, {40, 12}} {
		for _, releases := range []int{0, 5, 300} {
			m := demoModel(size[0], size[1], releases)
			for _, mode := range []string{"list", "log", "help"} {
				m.logView, m.help = mode == "log", mode == "help"
				lines := strings.Split(m.View(), "\n")
				if len(lines) != size[1] {
					t.Errorf("%dx%d %s releases=%d: %d lines, want %d", size[0], size[1], mode, releases, len(lines), size[1])
				}
				for i, line := range lines {
					if w := ansi.StringWidth(line); w != size[0] {
						t.Errorf("%dx%d %s releases=%d line %d: width %d, want %d: %q", size[0], size[1], mode, releases, i, w, size[0], ansi.Strip(line))
						break
					}
				}
			}
		}
	}
}

// TestNoColorIsPlainText checks the two ways a user switches color off. The
// theme is built while the package loads, so the variables are set before the
// test binary starts and the view must come out without a single escape sequence.
func TestNoColorIsPlainText(t *testing.T) {
	if os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb" {
		t.Skip("set NO_COLOR or TERM=dumb")
	}
	for _, size := range [][2]int{{120, 36}, {80, 24}} {
		m := demoModel(size[0], size[1], 30)
		view := m.View()
		if strings.Contains(view, "\x1b") {
			t.Errorf("%dx%d: NO_COLOR output still contains escape sequences:\n%s", size[0], size[1], view)
		}
		lines := strings.Split(view, "\n")
		if len(lines) != size[1] {
			t.Errorf("%dx%d: %d lines, want %d", size[0], size[1], len(lines), size[1])
		}
		// Below twoColumnsAt the list stands alone, so the detail column is only there when it fits.
		if !strings.Contains(view, "Track Nummer 1") || size[0] >= twoColumnsAt && !strings.Contains(view, "Playlist") {
			t.Errorf("%dx%d: the plain view lost its content", size[0], size[1])
		}
	}
}

func TestTooSmall(t *testing.T) {
	m := demoModel(30, 8, 5)
	if v := m.View(); !strings.Contains(v, "zu klein") {
		t.Errorf("got %q", v)
	}
}

func TestViewShowsTheRun(t *testing.T) {
	m := demoModel(120, 36, 30)
	view := ansi.Strip(m.View())
	for _, want := range []string{"harderstyles-radar", "Releases › Suchen", "Suche bei Spotify", "20 / 30", "Track Nummer 1", "● neu", "✓ drin", "✗ fehlt", "Playlist", "312", "/ 500", "12.01.2026", "Protokoll", "x abbrechen", "DATUM", "29.10.2026", "Anfragen"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q", want)
		}
	}
}

func TestResultAfterTheRun(t *testing.T) {
	m := demoModel(120, 36, 30)
	m.Update(doneMsg{res: sync.Result{Added: 7, Removed: 3, Unmatched: make([]hardstyle.Track, 2)}})
	m.Update(tickMsg(time.Now()))
	view := ansi.Strip(m.View())
	for _, want := range []string{"Fertig", "+7 neu · −3 entfernt · 2 fehlen · 0 warten", "in 23:59:5", "s synchronisieren"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q", want)
		}
	}

	m = demoModel(120, 36, 30)
	m.Update(doneMsg{err: fmt.Errorf("Playlist lesen: HTTP 403")})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "✗ Fehler") || !strings.Contains(view, "HTTP 403") {
		t.Errorf("error not shown:\n%s", view)
	}

	m = demoModel(120, 36, 30)
	m.Update(doneMsg{err: context.Canceled})
	if m.err != nil || m.running || !m.nextRun.IsZero() {
		t.Errorf("a cancelled run is no error and schedules nothing: err=%v running=%v next=%v", m.err, m.running, m.nextRun)
	}
}

func TestSelectionFollowsTheRunUntilMovedByHand(t *testing.T) {
	m := demoModel(120, 36, 300)
	if m.selected != 199 || m.offset == 0 {
		t.Fatalf("selected=%d offset=%d, want the marked line to follow to 199 and the list to scroll", m.selected, m.offset)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m.Update(eventMsg(event.Event{Kind: event.EvItem, Index: 250, Status: event.StatusNew}))
	if m.selected != 198 {
		t.Errorf("selected=%d, a move by hand must stop the following", m.selected)
	}
	if cur, ok := m.current(); !ok || cur.track.Title != "Track Nummer 199 (Extended Mix)" {
		t.Errorf("current = %+v", cur)
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Track Nummer 199") || strings.Contains(view, "Track Nummer 1 (") {
		t.Errorf("the list should be scrolled to the marked line")
	}
}

func TestGenreFilter(t *testing.T) {
	m := demoModel(120, 36, 30)
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	if m.filter != "Hardstyle" || len(m.shown()) != 10 {
		t.Errorf("filter=%q shown=%d", m.filter, len(m.shown()))
	}
	for i := 0; i < 3; i++ { // Hardcore, Uptempo, all
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	}
	if m.filter != "" { // Hardstyle, Hardcore, Uptempo, then all genres again
		t.Errorf("filter=%q after cycling, want all genres", m.filter)
	}
}

func TestKeysDuringRun(t *testing.T) {
	m := demoModel(120, 36, 30)
	cancelled := false
	m.cancel = func() { cancelled = true }
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if m.dryRun {
		t.Error("the mode must not change during a run")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if !cancelled {
		t.Error("x should cancel the run")
	}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}); cmd == nil {
		t.Error("q should quit")
	}
}

func TestRightClickOpensTheMenuAndEnterAppliesIt(t *testing.T) {
	m := demoModel(120, 36, 30)
	m.running = false
	m.items[0].track.ID = "111"
	// The demo follows the run to the end of the list; the test wants the top.
	m.selected, m.offset = 0, 0

	// The first release row is the first line of the list that rowAt accepts.
	list, _, _, _ := m.layout()
	row := -1
	for y := list.y; y < list.y+list.h; y++ {
		if pos, ok := m.rowAt(list.x+2, y); ok && pos == 0 {
			row = y
			break
		}
	}
	if row < 0 {
		t.Fatal("no row of the list accepts a click")
	}
	click := func(button tea.MouseButton) {
		m.Update(tea.MouseMsg{X: list.x + 2, Y: row, Button: button, Action: tea.MouseActionPress})
	}
	click(tea.MouseButtonRight)
	if m.menu == nil || m.menu.item != 0 {
		t.Fatalf("right click should open the menu for the first release, got %+v", m.menu)
	}

	var got []string
	m.opts.Correct = func(id, action, uri string) error {
		got = []string{id, action, uri}
		return nil
	}
	// The first action is "search again".
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !slices.Equal(got, []string{"111", corrections.Retry, ""}) || m.menu != nil {
		t.Errorf("enter recorded %v, menu open = %v", got, m.menu != nil)
	}

	// A typed Spotify track is taken as given, the prefix added when it was left off.
	click(tea.MouseButtonRight)
	for range len(m.menu.actions) - 1 {
		m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	for _, r := range "abc123" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !slices.Equal(got, []string{"111", corrections.Force, "spotify:track:abc123"}) {
		t.Errorf("a typed track recorded %v", got)
	}

	// A paste arrives as one message. A trailing newline from the clipboard must not become part of the id.
	click(tea.MouseButtonRight)
	for range len(m.menu.actions) - 1 {
		m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Paste: true, Runes: []rune("pasteID\n")})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !slices.Equal(got, []string{"111", corrections.Force, "spotify:track:pasteID"}) {
		t.Errorf("a pasted track recorded %v", got)
	}
}

func TestCorrectionKeysRecordTheMarkedRelease(t *testing.T) {
	m := demoModel(120, 36, 30)
	m.running = false
	m.items[0].track.ID = "111"
	m.selected = 0
	uri := m.items[0].uri

	var got []string
	m.opts.Correct = func(id, action, uri string) error {
		got = []string{id, action, uri}
		return nil
	}
	m.opts.Run = func(context.Context, bool, bool, event.Emit) (sync.Result, error) {
		return sync.Result{}, nil
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("u")})
	if !slices.Equal(got, []string{"111", "skip", uri}) {
		t.Errorf("skip recorded %v", got)
	}

	// A second model, still in its run: the key must not record anything.
	busy := demoModel(120, 36, 30)
	busy.items[0].track.ID = "111"
	busy.opts.Correct = m.opts.Correct
	got = nil
	busy.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if got != nil {
		t.Errorf("a correction during a run recorded %v", got)
	}
}

// Run with HS_DUMP=1 to see the frame as text.
func TestDump(t *testing.T) {
	if os.Getenv("HS_DUMP") == "" {
		t.Skip("set HS_DUMP=1")
	}
	m := demoModel(120, 30, 90)
	fmt.Println(ansi.Strip(m.View()))
	m.help = true
	fmt.Println(ansi.Strip(m.View()))
}

func TestRateLimitSchedulesTheRetry(t *testing.T) {
	m := demoModel(120, 36, 30)
	m.Update(doneMsg{err: fmt.Errorf("Suche: %w", spotify.NewRateLimitError(23*time.Hour))})
	if until := m.nextRun.Sub(m.lastRun); until < 23*time.Hour || until > 24*time.Hour {
		t.Errorf("next run in %s, want just after the 23h limit", until)
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "Spotify-Limit · wieder ab") {
		t.Errorf("view does not say why:\n%s", view)
	}
}

func TestLogViewTakesTheLeftColumn(t *testing.T) {
	m := demoModel(120, 36, 30)
	m.logView = true
	view := ansi.Strip(m.View())
	if n := strings.Count(view, "╭─ Protokoll"); n != 1 {
		t.Errorf("the log box is shown %d times, want once:\n%s", n, view)
	}
	if !strings.Contains(view, "╭─ Playlist") {
		t.Errorf("the right column must stay:\n%s", view)
	}
}

func TestGoesOnByItselfWhenWorkIsLeft(t *testing.T) {
	m := demoModel(120, 36, 30)
	m.opts.Every = 0 // no regular runs: only the leftover work and errors carry on
	m.opts.ContinueDelay = 10 * time.Minute
	m.opts.Run = func(context.Context, bool, bool, event.Emit) (sync.Result, error) { return sync.Result{}, nil }

	m.Update(doneMsg{res: sync.Result{Added: 40, Deferred: 120}})
	if m.running || m.nextRun.IsZero() || !strings.Contains(m.nextWhy, "120 warten") {
		t.Fatalf("running=%v next=%v why=%q, want the next run planned", m.running, m.nextRun, m.nextWhy)
	}
	m.Update(tickMsg(m.lastRun.Add(time.Second)))
	if view := ansi.Strip(m.View()); !strings.Contains(view, "Nächster Lauf in 10 Min. (120 warten)") || !strings.Contains(view, "in 00:09:59 · 120 warten") {
		t.Errorf("the countdown and its reason must be shown:\n%s", view)
	}

	// Before the time nothing happens, at the time the next run starts by itself.
	m.Update(tickMsg(m.nextRun.Add(-time.Minute)))
	if m.running {
		t.Fatal("started too early")
	}
	if _, cmd := m.Update(tickMsg(m.nextRun.Add(time.Second))); cmd == nil || !m.running {
		t.Errorf("running=%v: the next run must start by itself", m.running)
	}
}

func TestErrorsCarryOnWithGrowingPauses(t *testing.T) {
	m := demoModel(120, 36, 30)
	m.opts.Every, m.opts.ContinueDelay = 0, 10*time.Minute
	m.Update(doneMsg{err: errors.New("boom")})
	first := m.nextRun.Sub(m.lastRun)
	m.running = true
	m.Update(doneMsg{err: errors.New("boom")})
	second := m.nextRun.Sub(m.lastRun)
	if first < 9*time.Minute || first > 11*time.Minute || second < 19*time.Minute || second > 21*time.Minute {
		t.Errorf("pauses %s then %s, want about 10 and 20 minutes", first, second)
	}
	m.running = true
	m.Update(doneMsg{})
	if m.failures != 0 {
		t.Errorf("a good run resets the count of failures, got %d", m.failures)
	}
}

// The boxes, the rule and the background reach the edges of the terminal: no free column at the sides, no free
// line between the head, the boxes and the foot.

// The boxes, the rule and the background reach the edges of the terminal: no free column at the sides, no free
// line between the head, the boxes and the foot.
func TestBoxesReachTheEdgesOfTheTerminal(t *testing.T) {
	for _, size := range [][2]int{{200, 60}, {120, 36}, {100, 30}, {80, 24}} {
		m := demoModel(size[0], size[1], 30)
		lines := strings.Split(ansi.Strip(m.View()), "\n")
		foot := len(m.footer())
		rule := lines[1]
		if strings.Trim(rule, "─") != "" || len([]rune(rule)) != size[0] {
			t.Errorf("%dx%d: the rule must fill the whole line, got %q", size[0], size[1], rule)
		}
		last := lines[len(lines)-foot-1]
		if !strings.HasPrefix(lines[2], "╭") || !strings.HasSuffix(lines[2], "╮") || len([]rune(lines[2])) != size[0] {
			t.Errorf("%dx%d: the boxes must start at the left edge and end at the right one: %q", size[0], size[1], lines[2])
		}
		if !strings.HasPrefix(last, "╰") || !strings.HasSuffix(last, "╯") {
			t.Errorf("%dx%d: the last line above the foot must be the bottom of the boxes: %q", size[0], size[1], last)
		}
		for i, line := range lines[2 : len(lines)-foot] {
			if !strings.HasPrefix(line, "╭") && !strings.HasPrefix(line, "│") && !strings.HasPrefix(line, "╰") && line != "" && !strings.HasPrefix(line, " ") {
				t.Errorf("%dx%d line %d does not start with a box: %q", size[0], size[1], i+2, line)
			}
		}
		if lines[0][0] != ' ' || strings.TrimSpace(lines[len(lines)-1]) == "" {
			t.Errorf("%dx%d: the head has a little inset, the foot line must have text: %q / %q", size[0], size[1], lines[0], lines[len(lines)-1])
		}
	}
}

// withLanguage switches the language for one test.

// germanWords must not show up on an English screen.
var germanWords = []string{"Suche", "wählen", "Protokoll", "Hilfe", "beenden", "Lauf", "Titel", "Künstler", "Datum", "fehlt", "drin",
	"offen", "Anfragen", "Ältestes", "Neuestes", "Modus", "Umfang", "Hinweis", "Fertig", "Bereit", "Tasten", "warten", "neu ", "entfernt"}

func TestEnglishInterface(t *testing.T) {
	withLanguage(t, "en")
	m := demoModel(140, 40, 30)
	for _, mode := range []string{"list", "log", "help"} {
		m.logView, m.help = mode == "log", mode == "help"
		view := ansi.Strip(m.View())
		for _, w := range germanWords {
			if strings.Contains(view, w) {
				t.Errorf("%s: the English screen shows %q:\n%s", mode, w, view)
			}
		}
	}
	m.logView, m.help = false, false
	view := ansi.Strip(m.View())
	for _, want := range []string{"Releases › Searching", "Searching Spotify", "20 / 30", "DATE", "TITLE", "ARTIST", "STATUS", "29 Oct 2026",
		"● new", "✓ in list", "✗ missing", "Oldest", "12 Jan 2026", "Newest", "Requests", "Last run", "Next run", "Mode", "writes",
		"Log", "Date", "hardstyle.com", "↑/↓ select", "x cancel", "? Help", "q quit"} {
		if !strings.Contains(view, want) {
			t.Errorf("English screen lacks %q:\n%s", want, view)
		}
	}

	m.Update(doneMsg{res: sync.Result{Added: 7, Removed: 3, Unmatched: make([]hardstyle.Track, 2), Deferred: 4}})
	view = ansi.Strip(m.View())
	for _, want := range []string{"Done", "+7 new · −3 removed · 2 missing · 4 waiting", "s sync", "d Dry run"} {
		if !strings.Contains(view, want) {
			t.Errorf("English result lacks %q", want)
		}
	}

	m = demoModel(140, 40, 30)
	m.Update(doneMsg{err: fmt.Errorf("x: %w", &spotify.RateLimitError{RetryAfter: time.Hour, Until: time.Date(2026, 10, 30, 1, 4, 0, 0, time.UTC)})})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "Spotify limit · again from 30 Oct 01:04") {
		t.Errorf("the limit message is not English:\n%s", view)
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

func TestAppsLine(t *testing.T) {
	m := newModel(context.Background(), Options{Genres: testGenres, Pages: 4, MaxTracks: 500})
	if got := m.appsText(); got != "–" {
		t.Errorf("without apps: %q", got)
	}
	apps := []spotify.AppStatus{{ClientID: "a", InUse: true}, {ClientID: "b"}}
	m.opts.Apps = func() []spotify.AppStatus { return apps }
	if got := ansi.Strip(m.appsText()); !strings.Contains(got, "#1") || !strings.Contains(got, "2") {
		t.Errorf("free: %q", got)
	}
	apps[0].InUse, apps[1].InUse = false, true
	apps[0].BlockedUntil = time.Now().Add(time.Hour)
	if got := ansi.Strip(m.appsText()); !strings.Contains(got, "#2") || !strings.Contains(got, "1") {
		t.Errorf("one blocked: %q", got)
	}
	apps[1].BlockedUntil = time.Now().Add(2 * time.Hour)
	if got := ansi.Strip(m.appsText()); !strings.Contains(got, "2") || strings.Contains(got, "#") {
		t.Errorf("all blocked: %q", got)
	}
}
