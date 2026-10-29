package tui

import (
	"context"
	"errors"
	"fmt"
	"hardstylesync/internal/event"
	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/i18n"
	"hardstylesync/internal/spotify"
	"hardstylesync/internal/sync"
	"slices"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Version is the version of the tool, set by the main package.
var Version = "dev"

const (
	maxLog   = 500
	tickRate = 200 * time.Millisecond
)

// Options configures the interface.
type Options struct {
	Genres    []string
	Pages     int
	MaxTracks int
	// MaxSearches and RequestDelay are the limits that keep the Spotify requests few and slow.
	MaxSearches  int
	RequestDelay time.Duration
	DryRun       bool
	// Every is the pause between two complete runs; 0 runs only on request.
	Every time.Duration
	// ContinueDelay is the pause before the next run when work is left over (search limit) or a run failed.
	ContinueDelay time.Duration
	// Watch is the pause between two checks for new tracks (the service); 0 switches the checks off.
	Watch time.Duration
	// Apps reports the Spotify apps and their blocks; nil hides the line.
	Apps func() []spotify.AppStatus
	// Run does one run and reports its progress. A quick run only checks for new tracks.
	Run func(ctx context.Context, dryRun, quick bool, emit event.Emit) (sync.Result, error)
}

type item struct {
	track  hardstyle.Track
	status event.Status
	uri    string
	date   string // release date on hardstyle.com
	note   string // why the release is missing or waits
}

type logLine struct {
	at   time.Time
	text string
	err  bool
}

type (
	tickMsg  time.Time
	eventMsg event.Event
	doneMsg  struct {
		res sync.Result
		err error
	}
)

type model struct {
	opts    Options
	ctx     context.Context
	program *tea.Program
	width   int
	height  int
	now     time.Time
	spin    int

	items    []item
	selected int // index in the shown (filtered) list
	offset   int
	follow   bool // the marked line follows the release that was looked at last
	filter   string

	running   bool
	cancel    context.CancelFunc
	phase     event.Phase
	phaseMsg  string
	done      int
	total     int
	dryRun    bool
	err       error
	result    *sync.Result
	lastRun   time.Time
	lastFull  time.Time // the last full run, a check for new tracks is not one
	runQuick  bool      // the run that is going is a check for new tracks
	nextRun   time.Time
	nextQuick bool   // the next run is a check for new tracks
	nextWhy   string // why the next run is due, shown next to the countdown
	failures  int    // runs that failed in a row
	playlist  event.PlaylistStats
	hasList   bool
	requests  int // Spotify requests of this run

	logs    []logLine
	logView bool
	help    bool

	// splash shows the intro with the logo of hardstyle.com; the first run starts behind it.
	splash      bool
	splashFrame int
	splashScene *splashScene
	splashSize  [2]int // the window the logo was drawn for
}

func newModel(ctx context.Context, opts Options) *model {
	return &model{ctx: ctx, opts: opts, dryRun: opts.DryRun, now: time.Now(), follow: true, width: 120, height: 36}
}

func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{tea.Tick(tickRate, func(t time.Time) tea.Msg { return tickMsg(t) }), m.startRun(false)}
	if m.splash {
		cmds = append(cmds, splashCmds())
	}
	return tea.Batch(cmds...)
}

// startRun starts a run in the background. Its progress comes back as messages.
func (m *model) startRun(quick bool) tea.Cmd {
	if m.running {
		return nil
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	m.running, m.err, m.result = true, nil, nil
	m.items, m.selected, m.offset, m.follow = nil, 0, 0, true
	m.phase, m.phaseMsg, m.done, m.total, m.requests = event.PhaseFetch, "", 0, 0, 0
	m.nextRun, m.nextWhy, m.runQuick = time.Time{}, "", quick
	switch {
	case quick:
		m.addLog(i18n.T("Prüfung auf neue Tracks", "Check for new tracks"), false)
	case m.dryRun:
		m.addLog(i18n.T("Lauf gestartet (Testlauf, es wird nichts geschrieben)", "Run started (dry run, nothing is written)"), false)
	default:
		m.addLog(i18n.T("Lauf gestartet", "Run started"), false)
	}
	run, dry, program := m.opts.Run, m.dryRun, m.program
	return func() tea.Msg {
		res, err := run(ctx, dry, quick, func(ev event.Event) { program.Send(eventMsg(ev)) })
		return doneMsg{res: res, err: err}
	}
}

func (m *model) addLog(text string, isErr bool) {
	m.logs = append(m.logs, logLine{at: time.Now(), text: text, err: isErr})
	if len(m.logs) > maxLog {
		m.logs = m.logs[len(m.logs)-maxLog:]
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.keepVisible()
	case tickMsg:
		m.now = time.Time(msg)
		m.spin++
		next := tea.Tick(tickRate, func(t time.Time) tea.Msg { return tickMsg(t) })
		if !m.running && !m.nextRun.IsZero() && !m.now.Before(m.nextRun) {
			return m, tea.Batch(next, m.startRun(m.nextQuick))
		}
		return m, next
	case eventMsg:
		m.handleEvent(event.Event(msg))
	case doneMsg:
		m.finish(msg)
	case splashTickMsg:
		// The frames only run while the intro is shown.
		if m.splash {
			m.splashFrame++
			return m, splashTick()
		}
	case splashDoneMsg:
		m.splash = false
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *model) handleEvent(ev event.Event) {
	switch ev.Kind {
	case event.EvLog:
		m.addLog(ev.Msg, false)
	case event.EvPhase:
		m.phase, m.done, m.total = ev.Phase, ev.Done, ev.Total
		m.phaseMsg = ev.Msg
	case event.EvReleases:
		m.items = make([]item, len(ev.Releases))
		for i, t := range ev.Releases {
			m.items[i] = item{track: t}
		}
	case event.EvItem:
		if ev.Index < 0 || ev.Index >= len(m.items) {
			return
		}
		m.items[ev.Index].status, m.items[ev.Index].uri = ev.Status, ev.URI
		m.items[ev.Index].date, m.items[ev.Index].note = ev.Date, ev.Note
		if m.follow {
			m.selectItem(ev.Index)
		}
	case event.EvPlaylist:
		m.playlist, m.hasList = ev.Playlist, true
	case event.EvRequests:
		m.requests = ev.Done
	}
}

func (m *model) finish(d doneMsg) {
	m.running = false
	m.cancel()
	m.lastRun = time.Now()
	switch {
	case errors.Is(d.err, context.Canceled):
		m.phase, m.err = event.PhaseIdle, nil
		m.addLog(i18n.T("Lauf abgebrochen", "Run cancelled"), false)
		return
	case d.err != nil:
		m.err = d.err
		m.addLog(i18n.T("Fehler: ", "Error: ")+d.err.Error(), true)
	default:
		res := d.res
		m.result = &res
		m.phase = event.PhaseDone
	}
	// Work that is left over, a rate limit or an error do not end the program: it goes on by itself.
	var limit *spotify.RateLimitError
	if d.err != nil && !errors.As(d.err, &limit) {
		m.failures++
	} else if d.err == nil {
		m.failures = 0
	}
	if d.err == nil && !m.runQuick {
		m.lastFull = m.lastRun
	}
	sched := sync.Schedule{Every: m.opts.Every, Watch: m.opts.Watch, Continue: m.opts.ContinueDelay, DryRun: m.dryRun}
	if wait, why, quick := sched.Next(d.res, d.err, m.failures, m.lastRun.Sub(m.lastFull)); wait > 0 {
		m.nextRun, m.nextWhy, m.nextQuick = m.lastRun.Add(wait), why, quick
		m.addLog(fmt.Sprintf(i18n.T("Nächster Lauf in %s (%s)", "Next run in %s (%s)"), i18n.Duration(wait), why), false)
	}
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.splash {
		// Any key skips the intro, only quitting still quits.
		m.splash = false
		if s := msg.String(); s == "ctrl+c" || s == "q" {
			return m.quit()
		}
		return m, nil
	}
	if m.help {
		switch msg.String() {
		case "ctrl+c", "q":
			return m.quit()
		}
		m.help = false
		return m, nil
	}
	switch msg.String() {
	case "ctrl+c", "q":
		return m.quit()
	case "?", "h":
		m.help = true
	case "s":
		return m, m.startRun(false)
	case "x", "esc":
		if m.running {
			m.cancel()
		}
	case "d":
		if !m.running {
			m.dryRun = !m.dryRun
			if m.dryRun {
				m.addLog(i18n.T("Testlauf an: es wird nichts geschrieben", "Dry run on: nothing is written"), false)
			} else {
				m.addLog(i18n.T("Testlauf aus: der nächste Lauf schreibt in die Playlist", "Dry run off: the next run writes to the playlist"), false)
			}
		}
	case "g":
		m.cycleGenre()
	case "l":
		m.logView = !m.logView
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup":
		m.move(-m.listCapacity())
	case "pgdown":
		m.move(m.listCapacity())
	case "home":
		m.move(-len(m.items))
	case "end":
		m.move(len(m.items))
		m.follow = m.running
	}
	return m, nil
}

func (m *model) quit() (tea.Model, tea.Cmd) {
	if m.running {
		m.cancel()
	}
	return m, tea.Quit
}

// shown returns the indexes of the releases the list shows.
func (m *model) shown() []int {
	idx := make([]int, 0, len(m.items))
	for i, it := range m.items {
		if m.filter == "" || it.track.Genre == m.filter {
			idx = append(idx, i)
		}
	}
	return idx
}

func (m *model) cycleGenre() {
	if len(m.opts.Genres) < 2 {
		return
	}
	i := slices.Index(m.opts.Genres, m.filter)
	if i+1 < len(m.opts.Genres) {
		m.filter = m.opts.Genres[i+1]
	} else {
		m.filter = ""
	}
	m.selected, m.offset = 0, 0
	m.follow = false
}

// move moves the marked line. A move by hand stops the line from following the run.
func (m *model) move(delta int) {
	m.follow = false
	m.selected = min(max(m.selected+delta, 0), max(0, len(m.shown())-1))
	m.keepVisible()
}

// selectItem marks the release with the index of m.items, if it is shown.
func (m *model) selectItem(index int) {
	if pos := slices.Index(m.shown(), index); pos >= 0 {
		m.selected = pos
		m.keepVisible()
	}
}

// keepVisible scrolls the list so the marked line is in view.
func (m *model) keepVisible() {
	capacity := m.listCapacity()
	if m.selected < m.offset {
		m.offset = m.selected
	}
	if m.selected >= m.offset+capacity {
		m.offset = m.selected - capacity + 1
	}
	m.offset = max(0, m.offset)
}

// current returns the marked release.
func (m *model) current() (item, bool) {
	idx := m.shown()
	if m.selected < 0 || m.selected >= len(idx) {
		return item{}, false
	}
	return m.items[idx[m.selected]], true
}

func (m *model) percent() int {
	frac := 0.0
	if m.total > 0 {
		frac = float64(m.done) / float64(m.total)
	}
	switch m.phase {
	case event.PhaseFetch:
		return int(10 * frac)
	case event.PhaseSearch:
		return 10 + int(80*frac)
	case event.PhaseWrite:
		return 90 + int(10*frac)
	case event.PhaseDone:
		return 100
	}
	return 0
}

func (m *model) phaseLabel() string {
	switch {
	case m.err != nil:
		return i18n.T("Fehler", "Error")
	case m.phase == event.PhaseFetch && m.phaseMsg != "":
		return i18n.T("Lese hardstyle.com · ", "Reading hardstyle.com · ") + m.phaseMsg
	case m.phase == event.PhaseFetch:
		return i18n.T("Lese hardstyle.com", "Reading hardstyle.com")
	case m.phase == event.PhaseSearch:
		return i18n.T("Suche bei Spotify", "Searching Spotify")
	case m.phase == event.PhaseWrite:
		return i18n.T("Playlist aktualisieren", "Updating playlist")
	case m.phase == event.PhaseDone:
		return i18n.T("Fertig", "Done")
	}
	return i18n.T("Bereit", "Ready")
}

func (m *model) phaseShort() string {
	switch {
	case m.err != nil:
		return i18n.T("Fehler", "Error")
	case m.phase == event.PhaseFetch:
		return i18n.T("Lesen", "Reading")
	case m.phase == event.PhaseSearch:
		return i18n.T("Suchen", "Searching")
	case m.phase == event.PhaseWrite:
		return i18n.T("Schreiben", "Writing")
	case m.phase == event.PhaseDone:
		return i18n.T("Fertig", "Done")
	}
	return i18n.T("Bereit", "Ready")
}

func (m *model) countdown() string {
	if m.nextRun.IsZero() {
		if m.opts.Every > 0 && m.running {
			return i18n.T("nach dem Lauf", "after the run")
		}
		return i18n.T("nur auf Tastendruck", "only on keypress")
	}
	d := max(0, m.nextRun.Sub(m.now))
	return fmt.Sprintf("in %02d:%02d:%02d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
}
