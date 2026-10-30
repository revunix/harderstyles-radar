package tui

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// The look follows orangeflix-dl, in the colors of the two brands: the blue of hardstyle.com and the green of
// Spotify on a near black surface, with a light scheme for bright terminals.

// darkScheme tells whether the dark scheme is in use.
var darkScheme = detectDarkBackground()

// pick returns the color of the active scheme.
func pick(light, dark string) lipgloss.Color {
	if darkScheme {
		return lipgloss.Color(dark)
	}
	return lipgloss.Color(light)
}

// The gradient of the name and of the progress bar, from the blue of hardstyle.com to the green of Spotify.
var (
	gradientLight = [3]string{"#0541E3", "#0B8F9A", "#1DB954"}
	gradientDark  = [3]string{"#3D6BFF", "#2BC4B4", "#1ED760"}
)

type themeStyles struct {
	Background lipgloss.Style // surface of the whole interface
	Panel      lipgloss.Style // inside of the boxes
	Selected   lipgloss.Style // background of the marked line
	SelectedFg lipgloss.Style
	Divider    lipgloss.Style
	Title      lipgloss.Style
	Text       lipgloss.Style
	Muted      lipgloss.Style
	Subtle     lipgloss.Style
	Accent     lipgloss.Style
	AccentBold lipgloss.Style
	Success    lipgloss.Style
	Warning    lipgloss.Style
	Danger     lipgloss.Style
	BoxActive  lipgloss.Style // frame of the box the keys work in
	BoxQuiet   lipgloss.Style // frame of the other boxes
}

var theme = newTheme()

func init() {
	// lipgloss reads the environment once, before this package's variables are
	// set, so the profile is forced here. Tests that paint with color set their
	// own profile and are unaffected.
	if colorDisabled() {
		lipgloss.SetColorProfile(termenv.Ascii)
	}
}

func newTheme() themeStyles {
	if colorDisabled() {
		// Every style renders its text unchanged, so the layout stays and only
		// the color goes.
		return themeStyles{}
	}
	fg := func(c lipgloss.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }
	text := pick("#18181b", "#fafafa")
	bg := pick("#f8fafc", "#09090b")
	return themeStyles{
		Background: lipgloss.NewStyle().Background(bg),
		Panel:      lipgloss.NewStyle().Foreground(text).Background(bg),
		Selected:   lipgloss.NewStyle().Background(pick("#e3ebff", "#111c3d")),
		SelectedFg: fg(pick("#18181b", "#fafafa")),
		Divider:    fg(pick("#e4e4e7", "#27272a")),
		Title:      fg(text).Bold(true),
		Text:       fg(text),
		Muted:      fg(pick("#52525b", "#a1a1aa")),
		Subtle:     fg(pick("#71717a", "#71717a")),
		Accent:     fg(pick("#0541E3", "#7C9CFF")),
		AccentBold: fg(pick("#0541E3", "#7C9CFF")).Bold(true),
		Success:    fg(pick("#166534", "#bbf7d0")),
		Warning:    fg(pick("#9a5b00", "#fdba74")),
		Danger:     fg(pick("#b91c1c", "#fecaca")),
		BoxActive:  fg(pick("#0541E3", "#3D6BFF")),
		BoxQuiet:   fg(pick("#d4d4d8", "#3f3f46")),
	}
}

// colorDisabled reports whether the user switched color off. NO_COLOR is the
// convention every tool shares (https://no-color.org); TERM=dumb is what a
// terminal says when it cannot do color at all. Either one means plain text,
// so a log that records the output does not fill up with escape sequences.
func colorDisabled() bool {
	if _, off := os.LookupEnv("NO_COLOR"); off {
		return true
	}
	return os.Getenv("TERM") == "dumb"
}

// detectDarkBackground guesses the background of the terminal from COLORFGBG.
// The terminal is deliberately not asked with an OSC query: a terminal that does
// not answer would hold the start of the program up for seconds.
func detectDarkBackground() bool {
	parts := strings.Split(os.Getenv("COLORFGBG"), ";")
	if len(parts) < 2 {
		return true
	}
	background, err := strconv.Atoi(strings.TrimSpace(parts[len(parts)-1]))
	if err != nil {
		return true
	}
	return background <= 6 // 0 to 6 are dark, 7 to 15 are light
}

// gradientAt returns the color of the gradient at a position from 0 to 1.
func gradientAt(t float64) lipgloss.Color {
	stops := gradientLight
	if darkScheme {
		stops = gradientDark
	}
	t = min(max(t, 0), 1) * 2
	from, to, f := 0, 1, t
	if t > 1 {
		from, to, f = 1, 2, t-1
	}
	r1, g1, b1 := parseHex(stops[from])
	r2, g2, b2 := parseHex(stops[to])
	mix := func(a, b int) int { return a + int(float64(b-a)*f) }
	return lipgloss.Color(fmt.Sprintf("#%02X%02X%02X", mix(r1, r2), mix(g1, g2), mix(b1, b2)))
}

func parseHex(s string) (int, int, int) {
	v, _ := strconv.ParseUint(strings.TrimPrefix(s, "#"), 16, 32)
	return int(v>>16) & 0xFF, int(v>>8) & 0xFF, int(v) & 0xFF
}

// gradientText colors a text with the brand gradient.
func gradientText(text string) string {
	runes := []rune(text)
	var b strings.Builder
	for i, r := range runes {
		b.WriteString(lipgloss.NewStyle().Foreground(gradientAt(float64(i) / float64(max(1, len(runes)-1)))).Render(string(r)))
	}
	return b.String()
}

// sequenceOf returns the escape sequence a style starts with. With color switched
// off there is none, and the callers that bookend a line with it stay plain too.
func sequenceOf(style lipgloss.Style) string {
	if colorDisabled() {
		return ""
	}
	rendered := style.Render(" ")
	if i := strings.Index(rendered, "m"); i > 0 {
		return rendered[:i+1]
	}
	return ""
}

// withBackground starts the line with a background and brings it back after every reset,
// so the color does not break off behind a styled word.
func withBackground(line, background string) string {
	if background == "" {
		return line
	}
	return strings.ReplaceAll(background+line, "\x1b[0m", "\x1b[0m"+background)
}
