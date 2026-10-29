package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// TestSplashDrawsTheLogos checks that both logos cover a good part of their boxes and that the holes stay open:
// the bowl of the D of hardstyle.com and the bars of Spotify.
func TestSplashDrawsTheLogos(t *testing.T) {
	s := newSplashScene(200, 16)
	if s.height != 16 || s.width > 200 || s.spX+s.spW != s.width {
		t.Fatalf("scene is %dx%d", s.width, s.height)
	}
	share := func(grid [][]float64) float64 {
		sum, n := 0.0, 0
		for _, row := range grid {
			for _, c := range row {
				sum += c
				n++
			}
		}
		return sum / float64(n)
	}
	if c := share(s.hsCover); c < 0.15 || c > 0.6 {
		t.Errorf("the logo of hardstyle.com covers %.2f of its box", c)
	}
	if c := share(s.spCover); c < 0.3 || c > 0.75 {
		t.Errorf("the logo of Spotify covers %.2f of its box", c)
	}
	// The middle of the bowl of the D at the upper right (about 84.5, 12.4 in the view box) is a hole.
	scale := splashViewW / float64(s.hsW)
	if c := s.hsCover[int(12.4/scale)][int(84.5/scale)]; c > 0.5 {
		t.Errorf("the hole of the D is filled (%.2f)", c)
	}
	// The top bar of Spotify is cut out, the disc below the bars is green.
	r := float64(s.spW) / 2
	at := func(u, v float64) float64 { return s.spCover[int(float64(s.height)+v*r)][int(r+u*r)] }
	if c := at(0.04, -0.30); c > 0.3 {
		t.Errorf("the top bar of Spotify is filled (%.2f)", c)
	}
	if c := at(0, 0.75); c < 0.7 {
		t.Errorf("the disc of Spotify is open below the bars (%.2f)", c)
	}
}

// TestSplashFitsAndSkips checks that the intro fills the window and that a key ends it.
func TestSplashFitsAndSkips(t *testing.T) {
	m := newModel(context.Background(), Options{Genres: testGenres, Pages: 4, MaxTracks: 500})
	m.splash = true
	for _, size := range [][2]int{{120, 36}, {80, 24}, {40, 12}, {220, 60}} {
		m.width, m.height = size[0], size[1]
		for frame := range 50 {
			m.splashFrame = frame
			lines := strings.Split(m.View(), "\n")
			if len(lines) != m.height {
				t.Fatalf("%v: %d lines", size, len(lines))
			}
			for _, l := range lines {
				if w := ansi.StringWidth(l); w != m.width {
					t.Fatalf("%v: a line is %d wide", size, w)
				}
			}
		}
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if m.splash {
		t.Error("a key does not skip the intro")
	}
	if m.running {
		t.Error("the key that skips the intro must not do anything else")
	}
}
