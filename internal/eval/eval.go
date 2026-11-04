// Package eval measures how well the title matching (spotify.Decide) finds the Spotify track of a release. The
// cache keeps, for each release, the release as the site listed it and what the title searches found; the
// truth is the track an ISRC named or a correction chose. The matching is replayed on that without a request.
package eval

import (
	"fmt"
	"hardstylesync/internal/cache"
	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/i18n"
	"hardstylesync/internal/spotify"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Class is the outcome of the matching for one release.
type Class int

const (
	Correct     Class = iota // the track taken is the truth
	Wrong                    // another track was taken, or one a correction refused
	Uncertain                // two recordings fit equally well, none was taken
	Missed                   // nothing was taken, though the truth was among the tracks found
	Unreachable              // the truth was not among the tracks found: the search missed it, not the scoring
	Unlabeled                // no truth is known; shown, not scored
)

// Counts are the numbers of releases per class.
type Counts struct {
	Correct, Wrong, Uncertain, Missed, Unreachable, Unlabeled int
	// Reachable are the releases whose truth was among the tracks found.
	Reachable int
}

// Precision is the share of the tracks taken that are right; ok is false when none was taken.
func (c Counts) Precision() (p float64, ok bool) {
	return ratio(c.Correct, c.Correct+c.Wrong)
}

// Recall is the share of the releases whose truth was found that got it; ok is false when there are none.
func (c Counts) Recall() (r float64, ok bool) {
	return ratio(c.Correct, c.Reachable)
}

func ratio(n, of int) (float64, bool) {
	if of == 0 {
		return 0, false
	}
	return float64(n) / float64(of), true
}

func (c *Counts) add(class Class, reachable bool) {
	switch class {
	case Correct:
		c.Correct++
	case Wrong:
		c.Wrong++
	case Uncertain:
		c.Uncertain++
	case Missed:
		c.Missed++
	case Unreachable:
		c.Unreachable++
	case Unlabeled:
		c.Unlabeled++
	}
	if reachable && class != Unlabeled {
		c.Reachable++
	}
}

// Case is one release of the evaluation.
type Case struct {
	Title, Version, Artist string
	Class                  Class
	Truth                  string // the URI that is right; "" when unlabeled
	Picked                 string // the URI the matching took first; "" when none
}

// Point is the outcome for one pair of thresholds.
type Point struct {
	MinScore, MinMargin float64
	Default             bool // the package defaults
	Counts
}

// Report is the outcome of an evaluation.
type Report struct {
	MinScore, MinMargin float64 // the thresholds of Counts and Cases
	Counts
	Cases []Case  // ordered by class, then artist and title
	Sweep []Point // the grid, MinScore first
}

// The grid of the sweep. It contains the package defaults.
var (
	sweepScores  = []float64{0.5, spotify.DefaultMinScore, 1.5, 2, 2.5, 3}
	sweepMargins = []float64{0.25, 0.5, spotify.DefaultMinMargin, 1, 1.5}
)

// labeled is a release of the cache that can be replayed.
type labeled struct {
	release  hardstyle.Track
	details  hardstyle.Details
	found    []spotify.Track
	truth    string
	rejected []string
}

// Evaluate replays the title matching with the options on every release of the cache that kept what its
// searches found, and over a small grid of thresholds. Without o.ArtistIDs, the artists the cache learned are
// used, as a run does.
func Evaluate(c *cache.Cache, o spotify.MatchOptions) Report {
	if o.ArtistIDs == nil {
		o.ArtistIDs = c.ArtistIDs
	}
	var cases []labeled
	c.Each(func(e cache.Entry) {
		if e.Release == nil || len(e.Searched) == 0 {
			return
		}
		r := e.Release
		l := labeled{
			// No ISRC: the title matching is measured.
			release:  hardstyle.Track{Title: r.Title, Version: r.Version, Artist: r.Artist, Label: r.Label, Duration: r.Duration},
			details:  hardstyle.Details{Date: r.Date, Duration: r.Duration},
			truth:    e.Forced,
			rejected: e.Rejected,
		}
		if l.truth == "" && e.ByISRC {
			l.truth = e.URI
		}
		for _, s := range e.Searched {
			l.found = append(l.found, spotify.TrackFromStored(s))
		}
		cases = append(cases, l)
	})

	rep := Report{MinScore: or(o.MinScore, spotify.DefaultMinScore), MinMargin: or(o.MinMargin, spotify.DefaultMinMargin)}
	for _, l := range cases {
		cs, reachable := replay(l, o)
		rep.Counts.add(cs.Class, reachable)
		rep.Cases = append(rep.Cases, cs)
	}
	sort.SliceStable(rep.Cases, func(i, j int) bool {
		a, b := rep.Cases[i], rep.Cases[j]
		if a.Class != b.Class {
			return a.Class < b.Class
		}
		if a.Artist != b.Artist {
			return a.Artist < b.Artist
		}
		if a.Title != b.Title {
			return a.Title < b.Title
		}
		return a.Version < b.Version
	})

	for _, ms := range sweepScores {
		for _, mm := range sweepMargins {
			p := Point{MinScore: ms, MinMargin: mm, Default: ms == spotify.DefaultMinScore && mm == spotify.DefaultMinMargin}
			po := o
			po.MinScore, po.MinMargin = ms, mm
			for _, l := range cases {
				cs, reachable := replay(l, po)
				p.Counts.add(cs.Class, reachable)
			}
			rep.Sweep = append(rep.Sweep, p)
		}
	}
	return rep
}

func or(v, def float64) float64 {
	if v == 0 {
		return def
	}
	return v
}

// replay runs the matching on one release and tells its class, and whether its truth was among the tracks.
func replay(l labeled, o spotify.MatchOptions) (Case, bool) {
	look := spotify.Decide(l.release, l.details, l.found, o)
	cs := Case{Title: l.release.Title, Version: l.release.Version, Artist: l.release.Artist, Truth: l.truth}
	if len(look.URIs) > 0 {
		cs.Picked = look.URIs[0]
	}
	reachable := l.truth != "" && slices.ContainsFunc(l.found, func(t spotify.Track) bool { return t.URI == l.truth })
	switch {
	case l.truth == "":
		cs.Class = Unlabeled
	case cs.Picked != "" && slices.Contains(l.rejected, cs.Picked):
		cs.Class = Wrong
	case !reachable:
		cs.Class = Unreachable
	case cs.Picked == l.truth:
		cs.Class = Correct
	case cs.Picked != "":
		cs.Class = Wrong
	case look.Reason == spotify.ReasonUncertain:
		cs.Class = Uncertain
	default:
		cs.Class = Missed
	}
	return cs, reachable
}

// String is the report for the screen, in the language of the moment.
func (r Report) String() string {
	var b strings.Builder
	total := len(r.Cases)
	if total == 0 {
		b.WriteString(i18n.T("Noch keine Daten: die Auswertung braucht Läufe, die Releases über die ISRC gefunden haben (EVAL_SAMPLES), und deren Suche gespeichert wurde.\n",
			"No data yet: the evaluation needs runs that found releases by their ISRC (EVAL_SAMPLES), with their search kept.\n"))
		return b.String()
	}
	fmt.Fprintf(&b, i18n.T("Auswertung der Zuordnung über die Titelsuche: %d Releases mit gespeicherter Suche, %d davon ohne bekannte Lösung\n",
		"Evaluation of the title matching: %d releases with a stored search, %d of them without a known answer\n"), total, r.Unlabeled)
	fmt.Fprintf(&b, i18n.T("richtig %d, falsch %d, unsicher %d, verpasst %d, Suche verfehlt %d\n",
		"correct %d, wrong %d, uncertain %d, missed %d, search missed %d\n"),
		r.Correct, r.Wrong, r.Uncertain, r.Missed, r.Unreachable)
	fmt.Fprintf(&b, i18n.T("Präzision %s, Trefferquote %s (MinScore %.2f, MinMargin %.2f)\n",
		"precision %s, recall %s (MinScore %.2f, MinMargin %.2f)\n"),
		percent(r.Precision()), percent(r.Recall()), r.MinScore, r.MinMargin)

	for _, c := range r.Cases {
		if c.Class == Correct || c.Class == Unlabeled {
			continue
		}
		name := c.Artist + " – " + c.Title
		if c.Version != "" {
			name += " (" + c.Version + ")"
		}
		fmt.Fprintf(&b, "  %-*s %s\n", 12, className(c.Class), name)
		if c.Picked != "" {
			fmt.Fprintf(&b, i18n.T("      gewählt %s, richtig %s\n", "      picked %s, right %s\n"), c.Picked, c.Truth)
		} else {
			fmt.Fprintf(&b, i18n.T("      richtig %s\n", "      right %s\n"), c.Truth)
		}
	}

	// The grid of thresholds, with the points that land on the same numbers in one line: usually most of them do.
	b.WriteString(i18n.T("\nSchwellen (* = Voreinstellung):\n", "\nThresholds (* = default):\n"))
	type group struct {
		text   string
		points []string
		def    bool
	}
	var groups []*group
	byOutcome := map[string]*group{}
	for _, p := range r.Sweep {
		out := percent(p.Precision()) + "/" + percent(p.Recall()) + "/" + strconv.Itoa(p.Uncertain)
		g, ok := byOutcome[out]
		if !ok {
			g = &group{text: fmt.Sprintf("%s %s, %s %s, %s %d",
				i18n.T("Präzision", "precision"), percent(p.Precision()),
				i18n.T("Trefferquote", "recall"), percent(p.Recall()),
				i18n.T("unsicher", "uncertain"), p.Uncertain)}
			byOutcome[out] = g
			groups = append(groups, g)
		}
		g.points = append(g.points, fmt.Sprintf("%.2f/%.2f", p.MinScore, p.MinMargin))
		g.def = g.def || p.Default
	}
	for _, g := range groups {
		mark := " "
		if g.def {
			mark = "*"
		}
		fmt.Fprintf(&b, "  %s %s\n       %s %s\n", mark, g.text,
			i18n.T("MinScore/MinMargin:", "MinScore/MinMargin:"), strings.Join(g.points, " "))
	}
	return b.String()
}

func percent(v float64, ok bool) string {
	if !ok {
		return "–"
	}
	return fmt.Sprintf("%.1f %%", v*100)
}

func className(c Class) string {
	switch c {
	case Correct:
		return i18n.T("richtig", "correct")
	case Wrong:
		return i18n.T("falsch", "wrong")
	case Uncertain:
		return i18n.T("unsicher", "uncertain")
	case Missed:
		return i18n.T("verpasst", "missed")
	case Unreachable:
		return i18n.T("Suche verfehlt", "search missed")
	}
	return i18n.T("offen", "unlabeled")
}
