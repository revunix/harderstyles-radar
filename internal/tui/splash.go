package tui

import (
	"fmt"
	"hardstylesync/internal/i18n"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The intro follows orangeflix-dl: the logo of hardstyle.com on the left, an arrow in the middle and the logo of
// Spotify on the right, each in the colors of its brand. Everything is drawn into a canvas of pixels, two per
// cell, the upper and the lower half of a block glyph. The logo of hardstyle.com is wiped in, the arrow grows
// towards Spotify, the Spotify logo opens up, then the scene pulses to the kick of a hardstyle track at 150 BPM.

const (
	splashFrameTime = 80 * time.Millisecond
	splashDuration  = 3800 * time.Millisecond
	splashBeat      = 0.4 // seconds per kick at 150 BPM
	splashSamples   = 4   // samples per pixel and axis, so the thin strokes stay smooth
	splashMinCover  = 0.08
	// splashTextRows are the rows below the logos: blank, bars, blank, version, hint.
	splashTextRows = 5
)

// The timeline of the intro in seconds.
const (
	splashWipeEnd   = 0.9 // the logo of hardstyle.com is in
	splashArrowFrom = 0.6
	splashArrowEnd  = 1.3
	splashSpotFrom  = 1.1
	splashSpotEnd   = 1.6 // the Spotify logo is open, the kicks start
	splashShimmer   = 1.6 // seconds a shimmer takes to cross the logo of hardstyle.com
	splashFlow      = 0.8 // seconds a pulse takes to run along the arrow
)

// The colors of the brands: hardstyle.com draws its logo in anthracite on light grey and has a strong blue and a
// mint as accents, Spotify is green. On a dark page the logo is light and the blue a little lighter, so both stay
// visible.
var (
	hsInk       = string(pick("#1D1D1D", "#EBEBEB"))
	hsBlue      = string(pick("#0541E3", "#3D6BFF"))
	hsMint      = string(pick("#5FC27D", "#78F59E"))
	spGreen     = string(pick("#1DB954", "#1ED760"))
	splashBack  = string(pick("#f8fafc", "#09090b"))
	splashFlash = string(pick("#1D1D1D", "#FFFFFF"))
)

type (
	splashTickMsg struct{}
	splashDoneMsg struct{}
)

// The view box and the paths of the logo of hardstyle.com.
const splashViewW, splashViewH = 95.03, 65.61

var splashPaths = []string{
	"m48.6,54.91c-.28,0-1.08.03-2.24.1,2.24-5.62,4.84-12.57,8.23-17.27,2.19-3.04,2.36-4.92,1.76-5.93,3.68-.95,3.95-4.41-1.99-5.22-7.54-1.02-19.5,3.22-19.83,3.33,2.16-2.92,3.05-5.23,3-6.92l1.16-2.09h7.17l.08,3.92h6.65L51.82,0h-7.91l-11.14,19.69c-.84.05-1.68.19-2.49.41L35.66,0h-6.73l-2.42,9.05h-6.06L22.86,0h-6.73l-6.64,24.83h6.73l2.42-9.05h6.06l-2.16,8.08c-7.75,5.23-11.35,11.93-4.21,14.26,7.87,2.58,10.82,2.45,7.36,4.75s-18.53,13.45-18.49,12.13,7.13-6.69,11.99-9.74,1.32-5.31-3.35-2.9c-2.74,1.42-10.1,6.32-14.17,14.04s-.01,9.53,3.78,7.6c2.99-1.51,18.6-12.13,25.2-19.15-1.06,1.92-1.98,3.72-2.61,5.17-2.92,6.65,3.01,6.56,4.98,2.65,1.48-2.95,7.28-14.86,10.05-20.56,3.08-.5,6.21-.58,9.31-.23.17.03.33.05.5.07-1.9,2.03-4.48,6.47-6.93,9.49-3.26,4.01-3.16,1.95-1.8-.77,0,0,2.16-4.37,2.97-5.84s-1.15-2.28-6.33,6.3-3.32,12.29,2.23,8.51c0,0-1.04,2.84-2.15,5.73-5,.37-11.36.94-15.88,1.58-8.12,1.14-9.87,6.21-5.31,5.98,6.26.01,12.49.84,18.53,2.45,3.43,1.13,4.86-2.14,6.7-6.78,1-.05,2-.04,2.99.03,3.86.67,3.99-3.69.67-3.72m-3-46.4l.15,6.94h-4.01l3.87-6.94Zm-11.97,31.2c-.72-4.76-15.15-4.37-12.6-7.36,2.87-2.89,6.08-5.42,9.57-7.52,1.26-.71,2.05-.97,1.85-.38-.04.13-.09.26-.15.38-1.43,2.65-3.02,5.21-4.76,7.68-.77,1.13-.81,2.38,1.53,2.14,1.07-.1,4.54-.93,8.66-1.7-1.15,1.85-2.63,4.25-4.11,6.75m4.81,21.56c-.85,1.33-11.7-.54-11.63-1.19.04-.39,6.84-.95,12.74-1.27-.32.84-.69,1.66-1.11,2.46",
	"m84.97,45.24c-2.78-.1-11.39.97-18.38,1.84l2.1-4.98s11.28-.93,13.21-3.32-9.45-2.24-10.54-2.18c.31-.95.75-1.86,1.3-2.69.53-.49,10.63-1.06,12.9-3.23s-2.14-3.7-9.59-2.59-8.91,2.41-9.73,6.83l-6.22,12.94c-1.08.19-2.18.27-3.28.22-.24-.45,6.51-12.15,9.1-17.89s-2.51-6.36-6.91,4.15c0,0-5.65,8.88-8.07,13.03s-1.6,8.07,2.06,6.6,23.62-3.14,30.44-3.14,6.53-5.43,1.62-5.6",
	"m66.26,54.36c-.86,1.16-1.73,3.15-1.9,1.97s-1.13-2.79-1.77-.85c-.04.14-.09.29-.13.44-.24.21-.55.33-.86.32-.35-1.4-1.77-1.01-2.44-.2-.55.67-.92,1.46-1.07,2.32-.66.68-1.75,1.74-2.06,1.66-.45-.12.35-3.19.99-3.54.24-.13.27.3.25.66,0,.14.1.26.24.27.06,0,.12-.01.17-.05.85-.75.32-3.22-1.42-2.15-1.99,1.21-3.28,6.35-1.01,6.29,1.12-.12,2.14-.73,2.78-1.65.36,1.89,2.92,1.46,3.53-1.75.04-.19.06-.36.08-.52.15-.03.31-.08.45-.14-.41,1.93-.64,4.09-.08,4.09.78,0,1.35-2.7,1.35-2.7.35-1.26.55,2.24,1.26.77s.78-1.93.53.14.66,1.93,1.01.85,1.01-3.54,1.57-5.18-.61-2.2-1.47-1.04m-6.7,4.63c-.48-.13.26-1.51.26-1.51.77-1.11.21,1.63-.26,1.51",
	"m52.43,57.88c-.38.28-.54.77-.39,1.22.27.74,1.38-.24,1.3-1.05s-.91-.17-.91-.17",
	"m62.19,17.6h.59l1.52,7.24h6.74l-1.55-8.73c2.82-1.58,4.92-4.2,5.84-7.3h0C76.63,3.95,73.74,0,68.89,0h-8.73l-6.64,24.83h6.73l1.94-7.24Zm2.9-10.86h2c.5-.02.99.2,1.29.61.29.43.37.96.22,1.45-.35,1.17-1.39,2-2.61,2.06h-2l1.1-4.12Z",
	"m93.27,3.64c-1.85-2.4-4.75-3.76-7.78-3.63h-6.73l-6.64,24.83h6.73c6.84,0,13.86-5.54,15.69-12.38l.02-.08c.94-2.96.46-6.19-1.29-8.75m-5.45,8.75l-.02.08c-.46,1.59-1.41,2.98-2.72,3.99-1.25,1.02-2.81,1.6-4.43,1.65l3.04-11.36c3.12,0,4.97,2.52,4.13,5.64",
}

type splashPoint struct{ x, y float64 }

// splashShapes are the paths of the logo as closed polygons, one list of subpaths per path.
var splashShapes = func() [][][]splashPoint {
	shapes := make([][][]splashPoint, len(splashPaths))
	for i, path := range splashPaths {
		shapes[i] = parsePath(path)
	}
	return shapes
}()

// splashScene is the intro for one size of the terminal. The coverage of every pixel is worked out once, the
// colors of every frame are laid over it.
type splashScene struct {
	width, height int // cells; the canvas has width x 2*height pixels
	hsW, arX, arW int
	spX, spW      int
	hsCover       [][]float64 // the logo of hardstyle.com
	arCover       [][]float64 // the arrow
	spCover       [][]float64 // the logo of Spotify
}

// splashLayout returns the widths of the three parts for a height in cells. A cell is twice as high as it is
// wide and carries two pixels, so a pixel is about square.
func splashLayout(height int) (hsW, arW, spW, gap int) {
	px := float64(2 * height)
	hsW = int(math.Round(px * splashViewW / splashViewH))
	arW = max(4, int(math.Round(px*0.7)))
	spW = max(2, int(math.Round(px*0.86)))
	gap = max(2, height/3)
	return
}

// newSplashScene fits the scene into a box of cells.
func newSplashScene(maxW, maxH int) *splashScene {
	height := max(2, maxH)
	for height > 2 {
		hsW, arW, spW, gap := splashLayout(height)
		if hsW+arW+spW+2*gap <= maxW {
			break
		}
		height--
	}
	hsW, arW, spW, gap := splashLayout(height)
	px := 2 * height
	s := &splashScene{height: height, hsW: hsW, arW: arW, spW: spW}
	s.arX = hsW + gap
	s.spX = s.arX + arW + gap
	s.width = s.spX + spW
	s.hsCover = coverage(hsW, px)
	s.arCover = sampleCover(arW, px, arrowShape(float64(arW), float64(px)))
	s.spCover = sampleCover(spW, px, spotifyShape(float64(spW), float64(px)))
	return s
}

// sampleCover samples a shape into a grid of pixels; inside gets a point in pixels.
func sampleCover(cols, rows int, inside func(x, y float64) bool) [][]float64 {
	grid := make([][]float64, rows)
	for row := range grid {
		grid[row] = make([]float64, cols)
		for col := range grid[row] {
			hits := 0
			for sy := range splashSamples {
				for sx := range splashSamples {
					if inside(float64(col)+(float64(sx)+0.5)/splashSamples, float64(row)+(float64(sy)+0.5)/splashSamples) {
						hits++
					}
				}
			}
			grid[row][col] = float64(hits) / (splashSamples * splashSamples)
		}
	}
	return grid
}

// arrowShape is an arrow that points to the right: a shaft and a head that fill the box.
func arrowShape(w, h float64) func(x, y float64) bool {
	return func(x, y float64) bool {
		u, v := x/w, math.Abs(y-h/2)/h
		return (u <= 0.6 && v < 0.07) || (u >= 0.5 && v <= 0.24*(1-u)/0.5)
	}
}

// spotifyShape is the logo of Spotify: a disc with three curved bars cut out, the widest at the top.
func spotifyShape(w, h float64) func(x, y float64) bool {
	r := w / 2
	// The bars as height, curvature, half length and thickness, all in radii of the disc.
	bars := [][4]float64{{-0.33, 0.34, 0.62, 0.21}, {0.02, 0.27, 0.53, 0.17}, {0.34, 0.21, 0.44, 0.14}}
	return func(x, y float64) bool {
		u, v := (x-w/2)/r, (y-h/2)/r
		if u*u+v*v > 1 {
			return false
		}
		for _, b := range bars {
			curve := func(t float64) float64 { return b[0] + b[1]*t*t + 0.05*t }
			t := u - 0.04
			if math.Abs(t) <= b[2] && math.Abs(v-curve(t)) < b[3]/2 {
				return false
			}
			// The ends of the bars are round.
			for _, end := range []float64{-b[2], b[2]} {
				if math.Hypot(t-end, v-curve(end)) < b[3]/2 {
					return false
				}
			}
		}
		return true
	}
}

// coverage samples the logo of hardstyle.com into a grid of pixels, row by row: every sample row is cut with
// the outlines, the spans inside follow the nonzero rule of SVG.
func coverage(cols, rows int) [][]float64 {
	scale := max(splashViewW/float64(cols), splashViewH/float64(rows))
	offX := (float64(cols)*scale - splashViewW) / 2
	offY := (float64(rows)*scale - splashViewH) / 2
	grid := make([][]float64, rows)
	type crossing struct {
		x   float64
		dir int
	}
	for row := range grid {
		grid[row] = make([]float64, cols)
		for sy := range splashSamples {
			y := (float64(row)+(float64(sy)+0.5)/splashSamples)*scale - offY
			var spans [][2]float64
			for _, shape := range splashShapes {
				var cuts []crossing
				for _, poly := range shape {
					for i, a := range poly {
						b := poly[(i+1)%len(poly)]
						if (a.y <= y) == (b.y <= y) {
							continue
						}
						dir := 1
						if b.y < a.y {
							dir = -1
						}
						cuts = append(cuts, crossing{a.x + (y-a.y)/(b.y-a.y)*(b.x-a.x), dir})
					}
				}
				slices.SortFunc(cuts, func(p, q crossing) int { return cmpFloat(p.x, q.x) })
				winding := 0
				for i, c := range cuts {
					winding += c.dir
					if winding != 0 && i+1 < len(cuts) {
						spans = append(spans, [2]float64{c.x, cuts[i+1].x})
					}
				}
			}
			for col := range grid[row] {
				for sx := range splashSamples {
					x := (float64(col)+(float64(sx)+0.5)/splashSamples)*scale - offX
					for _, s := range spans {
						if x >= s[0] && x < s[1] {
							grid[row][col]++
							break
						}
					}
				}
			}
		}
		for col := range grid[row] {
			grid[row][col] /= splashSamples * splashSamples
		}
	}
	return grid
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// splashKick is the envelope of the kick: it hits at every beat and fades out quickly.
func splashKick(seconds float64) float64 {
	return math.Exp(-math.Mod(seconds, splashBeat) * 9)
}

func clamp01(v float64) float64 { return min(max(v, 0), 1) }

// lines draws the scene at a moment of the intro.
func (s *splashScene) lines(seconds float64) []string {
	canvas := make([][]string, 2*s.height)
	for y := range canvas {
		canvas[y] = make([]string, s.width)
	}
	kick := 0.0
	if seconds > splashSpotEnd {
		kick = splashKick(seconds - splashSpotEnd)
	}
	s.paintHardstyle(canvas, seconds, kick)
	s.paintArrow(canvas, seconds)
	s.paintSpotify(canvas, seconds, kick)
	return canvasLines(canvas)
}

// put lays a color with its coverage over the page.
func put(canvas [][]string, x, y int, color string, cover float64) {
	canvas[y][x] = mixHex(splashBack, color, min(1, cover*1.15))
}

// paintHardstyle draws the logo of hardstyle.com: wiped in slanted like its letters, then a mint shimmer runs
// over it and it pulses blue to the kick.
func (s *splashScene) paintHardstyle(canvas [][]string, seconds, kick float64) {
	rows := len(canvas)
	reveal := cubicOut(clamp01(seconds / splashWipeEnd))
	shimmer := -1.0
	if seconds > splashWipeEnd {
		shimmer = math.Mod(seconds-splashWipeEnd, splashShimmer)/splashShimmer*1.6 - 0.3
	}
	for y := range rows {
		for x := range s.hsW {
			cover := s.hsCover[y][x]
			if cover < splashMinCover {
				continue
			}
			u, v := float64(x)/float64(max(1, s.hsW-1)), float64(y)/float64(max(1, rows-1))
			front := reveal*1.25 - (u + 0.25*(1-v))
			if front < 0 {
				continue
			}
			color := hsInk
			if front < 0.08 && reveal < 1 {
				color = mixHex(color, hsBlue, 1-front/0.08)
			}
			if band := math.Abs(u + 0.35*v - shimmer); band < 0.12 {
				color = mixHex(color, hsMint, 0.75*(1-band/0.12))
			}
			put(canvas, x, y, mixHex(color, hsBlue, 0.45*kick), cover)
		}
	}
}

// paintArrow draws the arrow from hardstyle.com to Spotify: it grows to the right, runs from the blue of
// hardstyle.com into the green of Spotify, and pulses travel along it.
func (s *splashScene) paintArrow(canvas [][]string, seconds float64) {
	grow := cubicOut(clamp01((seconds - splashArrowFrom) / (splashArrowEnd - splashArrowFrom)))
	flow := (seconds - splashArrowEnd) / splashFlow
	for y := range canvas {
		for x := range s.arW {
			cover := s.arCover[y][x]
			if cover < splashMinCover {
				continue
			}
			u := float64(x) / float64(max(1, s.arW-1))
			if u > grow*1.02 {
				continue
			}
			color := mixHex(hsBlue, spGreen, u)
			if grow < 1 && grow-u < 0.12 {
				color = mixHex(color, "#FFFFFF", 0.6)
			}
			if flow > 0 {
				// Two pulses on the arrow at a time, half a length apart.
				for _, offset := range []float64{0, 0.5} {
					p := math.Mod(flow+offset, 1)*1.3 - 0.15
					if d := math.Abs(u - p); d < 0.12 {
						color = mixHex(color, "#FFFFFF", 0.55*(1-d/0.12))
					}
				}
			}
			put(canvas, s.arX+x, y, color, cover)
		}
	}
}

// paintSpotify draws the logo of Spotify: it opens up from its middle when the arrow arrives and flashes to the
// kick.
func (s *splashScene) paintSpotify(canvas [][]string, seconds, kick float64) {
	open := cubicOut(clamp01((seconds - splashSpotFrom) / (splashSpotEnd - splashSpotFrom)))
	r := float64(s.spW) / 2
	color := mixHex(spGreen, splashFlash, 0.3*kick)
	for y := range canvas {
		for x := range s.spW {
			cover := s.spCover[y][x]
			if cover < splashMinCover {
				continue
			}
			if math.Hypot(float64(x)+0.5-r, float64(y)+0.5-float64(len(canvas))/2)/r > open*1.05 {
				continue
			}
			put(canvas, s.spX+x, y, color, cover)
		}
	}
}

// canvasLines turns the canvas into rows of cells: the upper pixel is the color of the glyph, the lower one the
// background.
func canvasLines(canvas [][]string) []string {
	out := make([]string, len(canvas)/2)
	for row := range out {
		var b strings.Builder
		for col := range canvas[2*row] {
			top, bottom := canvas[2*row][col], canvas[2*row+1][col]
			switch {
			case top == "" && bottom == "":
				b.WriteByte(' ')
			case bottom == "":
				b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(top)).Render("▀"))
			case top == "":
				b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(bottom)).Render("▄"))
			case top == bottom:
				b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(top)).Render("█"))
			default:
				b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(top)).Background(lipgloss.Color(bottom)).Render("▀"))
			}
		}
		out[row] = b.String()
	}
	return out
}

// splashBars is a row of bars below the scene that jumps to the kick, loud in the middle and quiet at the sides,
// from the blue of hardstyle.com into the green of Spotify.
func splashBars(width int, seconds float64) string {
	glyphs := []rune("▁▂▃▄▅▆▇█")
	kick := 0.0
	if seconds > splashSpotEnd {
		kick = splashKick(seconds - splashSpotEnd)
	}
	var b strings.Builder
	for col := range width {
		x := float64(col) / float64(max(1, width-1))
		if x > seconds/splashArrowEnd {
			b.WriteByte(' ')
			continue
		}
		wobble := 0.5 + 0.5*math.Sin(float64(col)*1.7+seconds*11)
		level := math.Sin(math.Pi*x) * (0.25*wobble + 0.75*kick)
		glyph := glyphs[min(len(glyphs)-1, int(level*float64(len(glyphs))))]
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(mixHex(hsBlue, spGreen, x))).Render(string(glyph)))
	}
	return b.String()
}

// splashLines is the whole intro, centered in the window.
func (m *model) splashLines() []string {
	seconds := float64(m.splashFrame) * splashFrameTime.Seconds()
	if m.splashScene == nil || m.splashSize != [2]int{m.width, m.height} {
		m.splashScene = newSplashScene(m.width-4, min(20, m.height-splashTextRows-2))
		m.splashSize = [2]int{m.width, m.height}
	}
	scene := m.splashScene
	brand := func(color string) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true)
	}
	content := scene.lines(seconds)
	content = append(content, "", splashBars(scene.width, seconds), "",
		theme.Muted.Render("v"+Version+" · ")+brand(hsInk).Render("hardstyle.com")+theme.Muted.Render(" → ")+brand(spGreen).Render("Spotify"),
		theme.Subtle.Render(string([]rune(spinnerChars)[m.splashFrame%len([]rune(spinnerChars))])+" "+i18n.T("starte … (Taste überspringt)", "starting … (any key skips)")),
	)

	rows := make([]string, 0, m.height)
	for range max(0, (m.height-len(content))/2) {
		rows = append(rows, "")
	}
	for i, line := range content {
		w := scene.width
		if i >= scene.height+2 {
			w = lipgloss.Width(line)
		}
		rows = append(rows, strings.Repeat(" ", max(0, (m.width-w)/2))+line)
	}
	for len(rows) < m.height {
		rows = append(rows, "")
	}
	return rows[:m.height]
}

// splashCmds start the frames of the intro and its end.
func splashCmds() tea.Cmd {
	return tea.Batch(splashTick(), tea.Tick(splashDuration, func(time.Time) tea.Msg { return splashDoneMsg{} }))
}

func splashTick() tea.Cmd {
	return tea.Tick(splashFrameTime, func(time.Time) tea.Msg { return splashTickMsg{} })
}

func cubicOut(t float64) float64 {
	return 1 - math.Pow(1-t, 3)
}

// mixHex mixes two colors, amount runs from 0 (from) to 1 (to).
func mixHex(from, to string, amount float64) string {
	amount = min(max(amount, 0), 1)
	r1, g1, b1 := parseHex(from)
	r2, g2, b2 := parseHex(to)
	mix := func(a, b int) int { return a + int(math.Round(float64(b-a)*amount)) }
	return fmt.Sprintf("#%02X%02X%02X", mix(r1, r2), mix(g1, g2), mix(b1, b2))
}

// parsePath turns an SVG path into closed polygons, one per subpath. It knows the commands of the logo:
// moves, lines, cubic curves and their short form, absolute and relative.
func parsePath(path string) [][]splashPoint {
	tokens := pathTokens(path)
	var polys [][]splashPoint
	var poly []splashPoint
	var cur, start, ctrl splashPoint
	var cmd, last rune
	i := 0
	num := func() float64 {
		v, _ := strconv.ParseFloat(tokens[i], 64)
		i++
		return v
	}
	closePoly := func() {
		if len(poly) > 2 {
			polys = append(polys, poly)
		}
		poly = nil
	}
	cubic := func(c1, c2, end splashPoint) {
		const steps = 16
		for s := 1; s <= steps; s++ {
			t := float64(s) / steps
			u := 1 - t
			poly = append(poly, splashPoint{
				x: u*u*u*cur.x + 3*u*u*t*c1.x + 3*u*t*t*c2.x + t*t*t*end.x,
				y: u*u*u*cur.y + 3*u*u*t*c1.y + 3*u*t*t*c2.y + t*t*t*end.y,
			})
		}
		ctrl, cur = c2, end
	}
	for i < len(tokens) {
		if r := []rune(tokens[i])[0]; unicode.IsLetter(r) {
			cmd = r
			i++
		}
		rel := unicode.IsLower(cmd)
		at := func(p splashPoint) splashPoint {
			if rel {
				return splashPoint{cur.x + p.x, cur.y + p.y}
			}
			return p
		}
		upper := unicode.ToUpper(cmd)
		switch upper {
		case 'M':
			closePoly()
			cur = at(splashPoint{num(), num()})
			start = cur
			poly = append(poly, cur)
			// More pairs after a move are lines.
			cmd = 'L'
			if rel {
				cmd = 'l'
			}
		case 'L':
			cur = at(splashPoint{num(), num()})
			poly = append(poly, cur)
		case 'H':
			x := num()
			if rel {
				x += cur.x
			}
			cur = splashPoint{x, cur.y}
			poly = append(poly, cur)
		case 'V':
			y := num()
			if rel {
				y += cur.y
			}
			cur = splashPoint{cur.x, y}
			poly = append(poly, cur)
		case 'C':
			c1 := at(splashPoint{num(), num()})
			c2 := at(splashPoint{num(), num()})
			cubic(c1, c2, at(splashPoint{num(), num()}))
		case 'S':
			// The first control point mirrors the last one of the curve before, if there was one.
			c1 := cur
			if last == 'C' || last == 'S' {
				c1 = splashPoint{2*cur.x - ctrl.x, 2*cur.y - ctrl.y}
			}
			c2 := at(splashPoint{num(), num()})
			cubic(c1, c2, at(splashPoint{num(), num()}))
		case 'Z':
			closePoly()
			cur = start
		default:
			i++
		}
		if upper != 'C' && upper != 'S' {
			ctrl = cur
		}
		last = upper
	}
	closePoly()
	return polys
}

// pathTokens splits a path into its letters and numbers. A number ends at a sign or at a second point, as in
// ".33-63.2" or "1.08.03".
func pathTokens(path string) []string {
	var tokens []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			tokens = append(tokens, b.String())
			b.Reset()
		}
	}
	dot := false
	for _, r := range path {
		switch {
		case unicode.IsLetter(r):
			flush()
			tokens = append(tokens, string(r))
			dot = false
		case r == '-' || r == '+':
			flush()
			b.WriteRune(r)
			dot = false
		case r == '.':
			if dot {
				flush()
			}
			b.WriteRune(r)
			dot = true
		case unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			flush()
			dot = false
		}
	}
	flush()
	return tokens
}
