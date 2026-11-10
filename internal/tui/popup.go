package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"hardstylesync/internal/corrections"
	"hardstylesync/internal/i18n"
	"hardstylesync/internal/updater"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// handleMouse selects a release on a left click, opens the window on a right click, and scrolls with the
// wheel. A click outside an open window closes it.
func (m *model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.splash || m.help {
		return m, nil
	}
	if m.menu != nil {
		if row, inside := m.menu.hitAction(msg.X, msg.Y); inside && msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			m.menu.cursor = row
			if m.menu.actions[row] == actCustom {
				m.menu.typing = true
				return m, nil
			}
			m.applyMenu()
		} else if !inside && msg.Action == tea.MouseActionPress {
			m.menu = nil
		}
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.move(-1)
	case tea.MouseButtonWheelDown:
		m.move(1)
	case tea.MouseButtonLeft, tea.MouseButtonRight:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		if pos, ok := m.rowAt(msg.X, msg.Y); ok {
			m.selected = pos
			m.follow = false
			m.keepVisible()
			if msg.Button == tea.MouseButtonRight {
				m.openMenu(msg.X, msg.Y)
			}
		}
	}
	return m, nil
}

// handleMenuKey drives the open window: the arrows move, enter carries the action out, esc closes it, and
// while a Spotify track is typed every other key goes into the line.
func (m *model) handleMenuKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.menu
	if p.typing {
		switch msg.String() {
		case "esc":
			p.typing = false
		case "enter":
			m.applyMenu()
		case "backspace":
			if r := []rune(p.input); len(r) > 0 {
				p.input = string(r[:len(r)-1])
			}
		default:
			// A paste arrives as one message with every rune in it, typing as one rune per message.
			if msg.Paste || (msg.Type == tea.KeyRunes && utf8.RuneCountInString(msg.String()) == 1) {
				for _, r := range msg.Runes {
					if r == '\n' || r == '\r' || r < ' ' {
						continue
					}
					p.input += string(r)
				}
			}
		}
		return m, nil
	}
	switch msg.String() {
	case "esc", "q":
		m.menu = nil
	case "up", "k":
		p.cursor = max(0, p.cursor-1)
	case "down", "j":
		p.cursor = min(len(p.actions)-1, p.cursor+1)
	case "enter":
		if p.actions[p.cursor] == actCustom {
			p.typing = true
			return m, nil
		}
		m.applyMenu()
	}
	return m, nil
}

// What a right click on a release can do.
const (
	actRetry  = iota // drop the remembered track and look the release up again
	actSkip          // never take the current Spotify track again
	actForce         // take the track even though the date does not agree
	actCustom        // take a Spotify track the user types in
)

// popup is the small window over the list. It offers one action per line, and the last one takes a Spotify
// track the user types.
type popup struct {
	item    int    // index into m.items, the release it belongs to
	actions []int  // the actions offered, in order
	cursor  int    // the marked action
	typing  bool   // the last action is taking a Spotify track
	input   string // what was typed so far
	x, y    int    // where it is drawn
	w, h    int
}

// openMenu builds the window for the marked release. What it offers depends on what is known about it: a
// release without a Spotify track can only be searched again or given a track by hand.
func (m *model) openMenu(atX, atY int) {
	it, ok := m.current()
	if !ok {
		return
	}
	idx := m.shown()
	p := &popup{item: idx[m.selected], x: atX, y: atY}
	p.actions = []int{actRetry}
	if it.uri != "" {
		p.actions = append(p.actions, actSkip, actForce)
	}
	p.actions = append(p.actions, actCustom)
	p.w, p.h = 46, len(p.actions)+4
	if p.x+p.w > m.width {
		p.x = max(0, m.width-p.w)
	}
	if p.y+p.h > m.height {
		p.y = max(0, m.height-p.h)
	}
	m.menu = p
}

// menuLabel is the text of one action, in the language of the moment.
func menuLabel(action int) string {
	switch action {
	case actRetry:
		return i18n.T("Erneut suchen", "Search again")
	case actSkip:
		return i18n.T("Diesen Spotify-Track nie wieder", "Never this Spotify track")
	case actForce:
		return i18n.T("Trotzdem übernehmen", "Take it anyway")
	default:
		return i18n.T("Spotify-Track angeben …", "Enter a Spotify track …")
	}
}

// menuLines draws the window. The marked action is shown in the selection color; while a track is typed, the
// last line shows what was typed and a cursor.
func (p *popup) menuLines() []string {
	lines := make([]string, 0, p.h)
	lines = append(lines, theme.Muted.Render(i18n.T("Release korrigieren", "Correct release")))
	lines = append(lines, theme.Divider.Render(strings.Repeat("·", p.w-4)))
	for i, a := range p.actions {
		label := menuLabel(a)
		if a == actCustom && p.typing {
			label = p.input + "▏"
		}
		line := " " + label
		if i == p.cursor {
			line = theme.Selected.Render(padTo(line, p.w-4))
		} else {
			line = theme.Text.Render(line)
		}
		lines = append(lines, line)
	}
	lines = append(lines, theme.Muted.Render(i18n.T("esc schließen", "esc to close")))
	return lines
}

// hitAction tells which action a click inside the window lands on, and whether it landed inside at all.
func (p *popup) hitAction(x, y int) (int, bool) {
	if x < p.x || x >= p.x+p.w || y < p.y || y >= p.y+p.h {
		return 0, false
	}
	row := y - p.y - 3 // title, rule, then the actions; the frame takes one line
	if row < 0 || row >= len(p.actions) {
		return 0, true
	}
	return row, true
}

// rowAt tells which shown release a click in the list lands on. The list starts below the progress and the
// column head, so those lines count as no release.
func (m *model) rowAt(x, y int) (int, bool) {
	list, _, _, _ := m.layout()
	head := 4 // status line, meter, blank line, column head
	if x < list.x || x >= list.x+list.w || y <= list.y+head || y >= list.y+list.h-1 {
		return 0, false
	}
	pos := m.offset + (y - list.y - head - 1)
	idx := m.shown()
	if pos < 0 || pos >= len(idx) {
		return 0, false
	}
	return pos, true
}

// applyMenu carries out the marked action and closes the window. A track typed by hand is taken as it is,
// with the spotify:track: prefix added when it was left off.
func (m *model) applyMenu() {
	p := m.menu
	if p == nil || p.item < 0 || p.item >= len(m.items) {
		m.menu = nil
		return
	}
	it := m.items[p.item]
	action := p.actions[p.cursor]
	m.menu = nil
	switch action {
	case actRetry:
		m.record(it, corrections.Retry, "")
	case actSkip:
		m.record(it, corrections.Skip, it.uri)
	case actForce:
		m.record(it, corrections.Force, it.uri)
	case actCustom:
		uri := strings.TrimSpace(p.input)
		uri = strings.TrimPrefix(uri, "https://open.spotify.com/track/")
		uri = strings.SplitN(uri, "?", 2)[0]
		if uri == "" {
			return
		}
		if !strings.HasPrefix(uri, "spotify:track:") {
			uri = "spotify:track:" + uri
		}
		m.record(it, corrections.Force, uri)
	}
}

// record writes one correction and says so in the log. It does not start a run: the window is for deciding,
// the next run picks the correction up.
func (m *model) record(it item, action, uri string) {
	if m.opts.Correct == nil || it.track.ID == "" {
		m.addLog(i18n.T("Korrektur nicht möglich: keine Track-ID", "Correction not possible: no track id"), true)
		return
	}
	if err := m.opts.Correct(it.track.ID, action, uri); err != nil {
		m.addLog(fmt.Sprintf(i18n.T("Korrektur nicht gespeichert: %v", "Correction not saved: %v"), err), true)
		return
	}
	m.addLog(fmt.Sprintf(i18n.T("Korrektur für %s – %s gespeichert", "Correction for %s – %s saved"), it.track.Title, it.track.Artist), false)
}

// overlay draws the window on top of the finished view. It works in cells, not bytes, so a styled line keeps
// its place.
func (m *model) overlay(view string) string {
	rows := strings.Split(view, "\n")
	if p := m.updatePopup; p != nil {
		box := p.render()
		x := max(0, (m.width-p.w)/2)
		y := max(0, (m.height-p.h)/2)
		for i, line := range box {
			rowIdx := y + i
			if rowIdx >= 0 && rowIdx < len(rows) {
				rows[rowIdx] = splice(rows[rowIdx], x, line)
			}
		}
		return strings.Join(rows, "\n")
	}
	if p := m.menu; p != nil {
		box := renderBox(p.w, p.h, "", theme.BoxActive, p.menuLines())
		for i, line := range box {
			y := p.y + i
			if y < 0 || y >= len(rows) {
				continue
			}
			rows[y] = splice(rows[y], p.x, line)
		}
		return strings.Join(rows, "\n")
	}
	return view
}

// splice replaces the cells of a row from x on with a new piece, keeping the style of the rest.
func splice(row string, x int, piece string) string {
	left := ansi.Cut(row, 0, x)
	right := ansi.Cut(row, x+ansi.StringWidth(piece), ansi.StringWidth(row))
	return left + piece + right
}

type updatePopup struct {
	check    *updater.CheckResult
	updating bool
	status   string
	err      error
	done     bool
	w, h     int
}

func newUpdatePopup(check *updater.CheckResult) *updatePopup {
	return &updatePopup{
		check: check,
		w:     56,
		h:     9,
	}
}

func (p *updatePopup) render() []string {
	title := i18n.T("Update verfügbar", "Update available")
	if p.updating {
		title = i18n.T("Aktualisierung", "Updating")
	} else if p.done {
		title = i18n.T("Update erfolgreich", "Update successful")
	} else if p.err != nil {
		title = i18n.T("Update-Fehler", "Update error")
	}
	return renderBox(p.w, p.h, title, theme.BoxActive, p.lines(p.w-4))
}

func (p *updatePopup) lines(width int) []string {
	var lines []string
	switch {
	case p.done:
		lines = append(lines,
			theme.Success.Render(fmt.Sprintf(i18n.T("Erfolgreich auf %s aktualisiert!", "Successfully updated to %s!"), p.check.LatestVersion)),
			"",
			i18n.T("Bitte starte die App neu, um das Update zu nutzen.", "Please restart the app to use the update."),
			"",
			theme.AccentBold.Render("[Enter / Esc]")+" "+theme.Text.Render(i18n.T("Schließen", "Close")),
		)
	case p.err != nil:
		lines = append(lines,
			theme.Danger.Render(i18n.T("Aktualisierung fehlgeschlagen:", "Update failed:")),
			"",
			theme.Text.Render(fit(p.err.Error(), width-4)),
			"",
			theme.AccentBold.Render("[Esc]")+" "+theme.Text.Render(i18n.T("Schließen", "Close")),
		)
	case p.updating:
		msg := p.status
		if msg == "" {
			msg = i18n.T("Wird aktualisiert …", "Updating …")
		}
		lines = append(lines,
			theme.Accent.Render(i18n.T("Update wird installiert …", "Installing update …")),
			"",
			theme.Muted.Render(fit(msg, width-4)),
			"",
			theme.Subtle.Render(i18n.T("Bitte warten …", "Please wait …")),
		)
	default:
		lines = append(lines,
			fmt.Sprintf(i18n.T("Neue Version %s ist verfügbar!", "New version %s is available!"), theme.AccentBold.Render(p.check.LatestVersion)),
			theme.Muted.Render(fmt.Sprintf(i18n.T("(Aktuell: v%s)", "(Current: v%s)"), Version)),
			"",
			i18n.T("Möchtest du jetzt direkt aktualisieren?", "Do you want to update now?"),
			"",
			theme.AccentBold.Render("[u / Enter]")+" "+theme.Text.Render(i18n.T("Jetzt aktualisieren", "Update now"))+"    "+theme.Muted.Render("[Esc] ")+theme.Muted.Render(i18n.T("Später", "Later")),
		)
	}
	return lines
}
