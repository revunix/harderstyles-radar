package tui

import (
	"errors"
	"fmt"
	"hardstylesync/internal/event"
	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/i18n"
	"hardstylesync/internal/match"
	"hardstylesync/internal/spotify"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

const (
	margin       = 0 // the boxes reach the edges of the terminal
	textInset    = 1 // head and foot keep a little room at the sides
	gap          = 1
	listShare    = 0.62
	topShare     = 0.62
	minBottom    = 7
	twoColumnsAt = 100
	minWidth     = 40
	minHeight    = 12
)

type rect struct{ x, y, w, h int }

// layout calculates the boxes below the head. On a narrow or low terminal the list stands alone.
func (m *model) layout() (list, detail, logBox, info rect) {
	footer := len(m.footer())
	top := 2 // head and rule, the boxes start right below
	body := m.height - top - footer
	inner := m.width
	if m.width < twoColumnsAt || body < 2*minBottom+gap {
		return rect{margin, top, inner, max(3, body)}, rect{}, rect{}, rect{}
	}
	listW := int(float64(inner-gap) * listShare)
	rightW := inner - gap - listW
	bottomH := max(minBottom, int(float64(body-gap)*(1-topShare)))
	topH := body - gap - bottomH
	list = rect{margin, top, listW, topH}
	detail = rect{margin + listW + gap, top, rightW, topH}
	logBox = rect{margin, top + topH + gap, listW, bottomH}
	info = rect{margin + listW + gap, top + topH + gap, rightW, bottomH}
	return
}

// listCapacity is the number of releases that fit into the list.
func (m *model) listCapacity() int {
	list, _, _, _ := m.layout()
	return max(1, list.h-2-4) // frame, progress (2 lines), blank line and column head
}

func (m *model) View() string {
	if m.width < minWidth || m.height < minHeight {
		return fmt.Sprintf(i18n.T("Das Fenster ist zu klein (%dx%d), nötig sind mindestens %dx%d.", "The window is too small (%dx%d), at least %dx%d is needed."), m.width, m.height, minWidth, minHeight)
	}
	if m.splash {
		return m.paint(m.splashLines())
	}
	var rows []string
	rows = append(rows, m.header()...)

	list, detail, logBox, info := m.layout()
	switch {
	case m.help:
		rows = append(rows, renderBox(m.width, m.height-len(rows)-len(m.footer()), i18n.T("Hilfe", "Help"), theme.BoxActive, m.helpLines(m.width-4))...)
	case m.logView && detail.w == 0:
		rows = append(rows, renderBox(list.w, list.h, i18n.T("Protokoll", "Log"), theme.BoxActive, m.logLines(list.w-4, list.h-2))...)
	case m.logView:
		// The log takes the whole left column, the right column stays as it is.
		full := list.h + gap + logBox.h
		left := renderBox(list.w, full, i18n.T("Protokoll", "Log"), theme.BoxActive, m.logLines(list.w-4, full-2))
		right := append(append(m.detailBox(detail), ""), m.infoBox(info)...)
		rows = append(rows, zip(left, right)...)
	case detail.w == 0:
		rows = append(rows, m.listBox(list)...)
	default:
		rows = append(rows, zip(m.listBox(list), m.detailBox(detail))...)
		rows = append(rows, "")
		rows = append(rows, zip(m.logBoxLines(logBox), m.infoBox(info))...)
	}
	for len(rows) < m.height-len(m.footer()) {
		rows = append(rows, "")
	}
	rows = append(rows[:m.height-len(m.footer())], m.footer()...)
	return m.overlay(m.paint(rows))
}

// zip puts two columns of boxes next to each other, with a gap between them.
func zip(left, right []string) []string {
	out := make([]string, max(len(left), len(right)))
	leftW := 0
	if len(left) > 0 {
		leftW = ansi.StringWidth(left[0])
	}
	for i := range out {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out[i] = padTo(l, leftW) + strings.Repeat(" ", gap) + r
	}
	return out
}

// paint fills every row up to the width and paints the surface color behind it, so the interface reaches the
// edges of the terminal and looks the same in every terminal. Without color the reset is left off, otherwise
// every line would end in an escape sequence that does nothing.
func (m *model) paint(rows []string) string {
	out := make([]string, len(rows))
	if colorDisabled() {
		for i, row := range rows {
			row = fit(row, m.width)
			out[i] = row + strings.Repeat(" ", max(0, m.width-ansi.StringWidth(row)))
		}
		return strings.Join(out, "\n")
	}
	background := sequenceOf(theme.Background)
	for i, row := range rows {
		row = fit(row, m.width)
		row += strings.Repeat(" ", max(0, m.width-ansi.StringWidth(row)))
		out[i] = withBackground(row, background) + "\x1b[0m"
	}
	return strings.Join(out, "\n")
}

func (m *model) header() []string {
	mark := theme.Accent.Render("●")
	if m.running {
		mark = theme.Accent.Render(string([]rune(spinnerChars)[m.spin%utf8.RuneCountInString(spinnerChars)]))
	}
	steps := []string{"Releases", m.phaseShort()}
	if m.help {
		steps = []string{i18n.T("Hilfe", "Help")}
	}
	status := ""
	if m.dryRun {
		status = i18n.T("Testlauf", "Dry run")
	}
	count := fmt.Sprintf(i18n.T("%d Releases", "%d releases"), len(m.items))
	head := renderHeader(m.width-2*textInset, mark, steps, status, count, m.now.Format("15:04:05"))
	// The text has a little room at the sides, the rule below it goes all the way.
	return []string{strings.Repeat(" ", textInset) + head[0], theme.Divider.Render(strings.Repeat(boxH, m.width))}
}

func (m *model) hints() []keyHint {
	if m.help {
		return []keyHint{{i18n.T("beliebige Taste", "any key"), i18n.T("zurück", "back")}, {"q", i18n.T("beenden", "quit")}}
	}
	hints := []keyHint{{"↑/↓", i18n.T("wählen", "select")}}
	if m.running {
		hints = append(hints, keyHint{"x", i18n.T("abbrechen", "cancel")})
	} else {
		hints = append(hints, keyHint{"s", i18n.T("synchronisieren", "sync")}, keyHint{"d", i18n.T("Testlauf", "Dry run")})
		if m.opts.Correct != nil {
			hints = append(hints, keyHint{"r", i18n.T("neu suchen", "search again")}, keyHint{"u", i18n.T("überspringen", "skip")})
		}
	}
	if len(m.opts.Genres) > 1 {
		hints = append(hints, keyHint{"g", "Genre"})
	}
	return append(hints, keyHint{"l", i18n.T("Protokoll", "Log")}, keyHint{"?", i18n.T("Hilfe", "Help")}, keyHint{"q", i18n.T("beenden", "quit")})
}

func (m *model) footer() []string {
	lines := renderFooter(m.width-2*textInset, m.hints())
	for i := range lines {
		lines[i] = strings.Repeat(" ", textInset) + lines[i]
	}
	return lines
}

// listBox is the main box: the progress of the run on top, the releases below.
func (m *model) listBox(r rect) []string {
	inner := r.w - 4
	title := fmt.Sprintf("Releases (%d)", len(m.shown()))
	if m.filter != "" {
		title = fmt.Sprintf("%s (%d)", m.filter, len(m.shown()))
	}

	content := []string{m.statusLine(inner), renderMeter(inner, m.percent()), ""}

	cols := m.columns(inner)
	head := strings.Repeat(" ", rowIndent) + padStyled(theme.Muted, "#", cols.idx) + " "
	if cols.genre > 0 {
		head += padStyled(theme.Muted, "GENRE", cols.genre) + " "
	}
	if cols.date > 0 {
		head += padStyled(theme.Muted, i18n.T("DATUM", "DATE"), cols.date) + " "
	}
	head += padStyled(theme.Muted, i18n.T("TITEL", "TITLE"), cols.title) + " " + padStyled(theme.Muted, i18n.T("KÜNSTLER", "ARTIST"), cols.artist) + " " + theme.Muted.Render("STATUS")
	content = append(content, head)

	idx := m.shown()
	if len(idx) == 0 {
		hint := i18n.T("Noch keine Releases.", "No releases yet.")
		if m.running {
			hint = i18n.T("Die Releases werden gelesen …", "Reading the releases …")
		}
		return renderBox(r.w, r.h, title, theme.BoxActive, append(content, theme.Muted.Render(hint)))
	}
	for pos := m.offset; pos < len(idx) && pos < m.offset+m.listCapacity(); pos++ {
		it := m.items[idx[pos]]
		row := padStyled(theme.Muted, fmt.Sprint(idx[pos]+1), cols.idx) + " "
		if cols.genre > 0 {
			row += padStyled(theme.Muted, it.track.Genre, cols.genre) + " "
		}
		if cols.date > 0 {
			row += padStyled(theme.Muted, shortDate(it.date), cols.date) + " "
		}
		row += padStyled(theme.Text, displayTitle(it.track), cols.title) + " " + padStyled(theme.Muted, it.track.Artist, cols.artist) + " " + statusBadge(it.status)
		content = append(content, renderRow(row, pos == m.selected, inner))
	}
	return renderBox(r.w, r.h, title, theme.BoxActive, content)
}

type columns struct{ idx, genre, date, title, artist int }

const statusWidth = 13

func (m *model) columns(inner int) columns {
	c := columns{idx: max(3, len(fmt.Sprint(len(m.items))))}
	if len(m.opts.Genres) > 1 && m.filter == "" && inner >= 84 {
		c.genre = 11
	}
	if inner >= 60 {
		// The date column follows the language: "29 Oct 2026" is one cell wider than "29.10.2026".
		c.date = utf8.RuneCountInString(i18n.Date(time.Date(2026, 12, 29, 0, 0, 0, 0, time.UTC)))
	}
	free := inner - rowIndent - statusWidth - c.idx - 1 - 2 // gaps between the columns
	if c.genre > 0 {
		free -= c.genre + 1
	}
	if c.date > 0 {
		free -= c.date + 1
	}
	c.title = max(8, free*55/100)
	c.artist = max(6, free-c.title)
	return c
}

// statusLine is the first line of the main box: what the run does, and its numbers on the right.
func (m *model) statusLine(width int) string {
	var icon, numbers string
	switch {
	case m.err != nil:
		icon = theme.Danger.Render("✗")
		numbers = theme.Danger.Render(fit(m.err.Error(), width/2))
		var limit *spotify.RateLimitError
		if errors.As(m.err, &limit) {
			numbers = theme.Danger.Render(i18n.T("Spotify-Limit · wieder ab ", "Spotify limit · again from ") + i18n.Stamp(limit.Until))
		}
	case m.running:
		icon = theme.Accent.Render(string([]rune(spinnerChars)[m.spin%utf8.RuneCountInString(spinnerChars)]))
		numbers = theme.Muted.Render(fmt.Sprintf("%d / %d · %d%%", m.done, m.total, m.percent()))
	case m.result != nil:
		icon = theme.Success.Render("✓")
		r := m.result
		numbers = theme.Muted.Render(fmt.Sprintf(i18n.T("+%d neu · −%d entfernt · %d fehlen · %d warten", "+%d new · −%d removed · %d missing · %d waiting"), r.Added, r.Removed, len(r.Unmatched), r.Deferred))
	default:
		icon = theme.Muted.Render("○")
	}
	left := icon + " " + theme.Title.Render(m.phaseLabel())
	room := width - ansi.StringWidth(numbers) - 1
	left = fit(left, max(1, room))
	return left + strings.Repeat(" ", max(1, width-ansi.StringWidth(left)-ansi.StringWidth(numbers))) + numbers
}

func (m *model) detailBox(r rect) []string {
	it, ok := m.current()
	if !ok {
		return renderBox(r.w, r.h, "Release", theme.BoxQuiet, []string{theme.Muted.Render(i18n.T("Noch kein Release gewählt.", "No release selected yet."))})
	}
	inner := r.w - 4
	content := []string{
		detailRow(i18n.T("Titel", "Title"), displayTitle(it.track)),
		detailRow(i18n.T("Künstler", "Artist"), it.track.Artist),
		detailRow(i18n.T("Datum", "Date"), shortDate(it.date)+" "+theme.Muted.Render("hardstyle.com")),
		detailRow("Status", statusBadge(it.status)),
	}
	if it.track.Genre != "" {
		content = append(content[:2], append([]string{detailRow("Genre", it.track.Genre)}, content[2:]...)...)
	}
	if it.uri != "" {
		content = append(content, detailRow("Spotify", theme.Muted.Render(strings.TrimPrefix(it.uri, "spotify:track:"))))
	}
	content = append(content, "", theme.Muted.Render(i18n.T("Hinweis", "Hint")))
	for _, line := range strings.Split(ansi.Wrap(m.hintFor(it), inner, ""), "\n") {
		content = append(content, theme.Text.Render(line))
	}
	return renderBox(r.w, r.h, it.track.Title, theme.BoxQuiet, content)
}

func (m *model) hintFor(it item) string {
	// The extra versions are no reason for the release itself, so the note about them does not replace the hint.
	if it.note != "" && !strings.HasPrefix(it.note, "+") {
		if it.status == event.StatusNotFound || it.status == event.StatusMismatch {
			return it.note + i18n.T(". Beim nächsten Lauf wird erneut gesucht.", ". It is searched again at the next run.")
		}
		return it.note + "."
	}
	switch it.status {
	case event.StatusNew:
		if m.dryRun {
			return i18n.T("Gefunden. Im Testlauf wird nichts geschrieben, sonst käme der Track in die Playlist.", "Found. A dry run writes nothing, otherwise the track would go into the playlist.")
		}
		return i18n.T("Gefunden. Der Track kommt in die Playlist.", "Found. The track goes into the playlist.")
	case event.StatusInPlaylist:
		return i18n.T("Der Track ist schon in der Playlist.", "The track is already in the playlist.")
	case event.StatusAdded:
		return i18n.T("Der Track wurde in diesem Lauf zur Playlist hinzugefügt.", "The track was added to the playlist in this run.")
	case event.StatusNotFound:
		return i18n.T("Kein sicherer Treffer bei Spotify.", "No confident match on Spotify.")
	}
	if !m.running && m.phase != event.PhaseIdle && it.note != "" {
		return i18n.T("Kommt beim nächsten Lauf dran, damit Spotify nicht zu oft gefragt wird.", "Comes up at the next run, so Spotify is not asked too often.")
	}
	return i18n.T("Wartet auf die Suche bei Spotify.", "Waiting for the search on Spotify.")
}

// infoBox shows the playlist and the schedule.
func (m *model) infoBox(r rect) []string {
	inner := r.w - 4
	count, oldest, newest := "–", "–", "–"
	percent := 0
	if m.hasList {
		count = fmt.Sprint(m.playlist.Count)
		percent = m.playlist.Count * 100 / max(1, m.opts.MaxTracks)
		if !m.playlist.Oldest.IsZero() {
			oldest, newest = i18n.Date(m.playlist.Oldest), i18n.Date(m.playlist.Newest)
		}
	}
	countStyle := theme.Text
	if m.hasList && m.playlist.Count >= m.opts.MaxTracks {
		countStyle = theme.Accent
	}
	last := "–"
	if !m.lastRun.IsZero() {
		last = m.lastRun.Format("15:04:05")
	}
	genres := fmt.Sprintf("%d Genres · %d × 30", len(m.opts.Genres), m.opts.Pages)
	if len(m.opts.Genres) == 1 {
		genres = fmt.Sprintf("%s · %d × 30", hardstyle.GenreLabel(m.opts.Genres[0]), m.opts.Pages)
	}
	mode := theme.Success.Render(i18n.T("schreibt", "writes"))
	if m.dryRun {
		mode = theme.Warning.Render(i18n.T("Testlauf", "Dry run"))
	}
	content := []string{
		detailRow("Tracks", countStyle.Render(count)+theme.Muted.Render(fmt.Sprintf(" / %d", m.opts.MaxTracks))),
		renderMeter(inner, percent),
		detailRow(i18n.T("Ältestes", "Oldest"), oldest),
		detailRow(i18n.T("Neuestes", "Newest"), newest),
		detailRow(i18n.T("Anfragen", "Requests"), fmt.Sprintf("%d", m.requests)+theme.Muted.Render(fmt.Sprintf(i18n.T(" · alle %s", " · every %s"), m.opts.RequestDelay.Round(10*time.Millisecond)))),
		detailRow(i18n.T("Letzter Lauf", "Last run"), last),
		detailRow("Spotify-Apps", m.appsText()),
		detailRow(i18n.T("Nächster Lauf", "Next run"), m.countdown()+m.nextWhyText()),
		detailRow(i18n.T("Modus", "Mode"), mode),
		detailRow(i18n.T("Umfang", "Scope"), genres),
	}
	return renderBox(r.w, r.h, "Playlist", theme.BoxQuiet, content)
}

// appsText sums up the Spotify apps: how many there are, which is in use and until when the blocked ones wait.
func (m *model) appsText() string {
	if m.opts.Apps == nil {
		return "–"
	}
	apps := m.opts.Apps()
	free, inUse := 0, 0
	var earliest time.Time
	for i, a := range apps {
		if a.InUse {
			inUse = i + 1
		}
		if a.BlockedUntil.IsZero() {
			free++
		} else if earliest.IsZero() || a.BlockedUntil.Before(earliest) {
			earliest = a.BlockedUntil
		}
	}
	switch {
	case free == 0 && len(apps) > 0:
		return theme.Warning.Render(fmt.Sprintf(i18n.T("alle %d gesperrt", "all %d blocked"), len(apps))) +
			theme.Muted.Render(fmt.Sprintf(i18n.T(" · bis %s", " · until %s"), i18n.Stamp(earliest)))
	case free < len(apps):
		return fmt.Sprintf(i18n.T("#%d aktiv", "#%d in use"), inUse) +
			theme.Muted.Render(fmt.Sprintf(i18n.T(" · %d/%d gesperrt", " · %d/%d blocked"), len(apps)-free, len(apps)))
	default:
		return fmt.Sprintf(i18n.T("#%d aktiv", "#%d in use"), inUse) + theme.Muted.Render(fmt.Sprintf(i18n.T(" · %d hinterlegt", " · %d configured"), len(apps)))
	}
}

func (m *model) logBoxLines(r rect) []string {
	return renderBox(r.w, r.h, i18n.T("Protokoll", "Log"), theme.BoxQuiet, m.logLines(r.w-4, r.h-2))
}

// logLines returns the newest lines of the log, the newest at the bottom.
func (m *model) logLines(width, height int) []string {
	logs := m.logs
	if len(logs) > height {
		logs = logs[len(logs)-height:]
	}
	lines := make([]string, 0, len(logs))
	for _, l := range logs {
		style := theme.Text
		if l.err {
			style = theme.Danger
		}
		lines = append(lines, theme.Muted.Render(l.at.Format("15:04:05"))+" "+style.Render(fit(l.text, max(1, width-9))))
	}
	return lines
}

func (m *model) helpLines(width int) []string {
	keys := [][2]string{
		{"↑/↓  j/k", i18n.T("Release wählen", "Select release")},
		{"PgUp/PgDn", i18n.T("seitenweise blättern", "scroll by page")},
		{"Home/End", i18n.T("zum Anfang oder Ende", "to the start or end")},
		{"s", i18n.T("jetzt synchronisieren", "sync now")},
		{"x / Esc", i18n.T("laufenden Lauf abbrechen", "cancel the running run")},
		{"d", i18n.T("Testlauf an oder aus (schreibt nichts)", "dry run on or off (writes nothing)")},
		{"r", i18n.T("markiertes Release neu suchen", "search the marked release again")},
		{"u", i18n.T("Spotify-Track des markierten Releases nie wieder nehmen", "never take the Spotify track of the marked release")},
		{"g", i18n.T("nach Genre filtern", "filter by genre")},
		{"l", i18n.T("Protokoll statt Releases zeigen", "show the log instead of the releases")},
		{"?", i18n.T("diese Hilfe", "this help")},
		{"q / Ctrl+C", i18n.T("beenden", "quit")},
	}
	lines := []string{theme.Muted.Render(i18n.T("Tasten", "Keys")), ""}
	for _, k := range keys {
		lines = append(lines, theme.AccentBold.Render(padTo(k[0], 12))+" "+theme.Text.Render(k[1]))
	}
	paragraph := func(text string) {
		for _, line := range strings.Split(ansi.Wrap(text, max(20, width), ""), "\n") {
			lines = append(lines, theme.Muted.Render(line))
		}
	}
	lines = append(lines, "")
	paragraph(fmt.Sprintf(i18n.T("Die Playlist hat dieselbe Reihenfolge wie hardstyle.com (neueste zuerst) und höchstens %d Tracks, die ältesten am Ende fallen weg.",
		"The playlist has the same order as hardstyle.com (newest first) and at most %d tracks, the oldest at the end drop out."), m.opts.MaxTracks))
	paragraph(fmt.Sprintf(i18n.T("Spotify wird nur langsam gefragt: höchstens %d Suchen pro Lauf. Bleibt etwas übrig, geht das Tool nach %s von selbst weiter, ebenso nach einem Fehler oder einer Spotify-Sperre.",
		"Spotify is asked slowly: at most %d searches per run. If something is left over, the tool carries on by itself after %s, as it does after an error or a Spotify block."), m.opts.MaxSearches, i18n.Duration(m.opts.ContinueDelay)))
	if m.opts.Every > 0 {
		paragraph(i18n.T("Neuer Lauf ", "New run ") + everyText(m.opts.Every) + ".")
	}
	return lines
}

// everyText says how often the runs repeat: "alle 24 Stunden".
func everyText(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf(i18n.T("alle %d Stunden", "every %d hours"), int(d/time.Hour))
	}
	return i18n.T("alle ", "every ") + d.Round(time.Minute).String()
}

// shortDate turns "2026-10-29" into "29.10.2026"; an unknown date is a dash.
func shortDate(date string) string {
	if d, err := time.Parse("2006-01-02", date); err == nil {
		return i18n.Date(d)
	}
	return "–"
}

// nextWhyText says why the next run is due, behind the countdown.
func (m *model) nextWhyText() string {
	if m.nextWhy == "" || m.nextRun.IsZero() {
		return ""
	}
	return theme.Muted.Render(" · " + m.nextWhy)
}

// displayTitle is the title with its version when the version says something: "Everything Is Looking Good
// (Fiesta Mix)". Versions like "Extended Mix" or "Radio Edit" are left out, they are on almost every track.
func displayTitle(t hardstyle.Track) string {
	if t.Version == "" || match.NormTitle(t.Version) == "" {
		return t.Title
	}
	return t.Title + " (" + t.Version + ")"
}
